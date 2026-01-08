package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"log"
	"os"
	"time"
)

const maxDurWithoutHashing time.Duration = 10 * time.Minute

var defaultInterval = 5 * time.Second

type FileChangedHandler func(file []byte)
type ErrorHandler func(err error)
type FileWatcher struct {
	filePath           string
	Interval           time.Duration
	FileChangedHandler FileChangedHandler
	ErrorHandler       ErrorHandler
	bytesCh            chan []byte
	errorCh            chan error
	lastSize           int64
	lastFileModTime    time.Time
	lastHash           []byte
	lastHashingTime    time.Time
}

func NewFileWatcher(filePath string) *FileWatcher {
	return &FileWatcher{
		filePath: filePath,
		errorCh:  make(chan error, 1),
		bytesCh:  make(chan []byte, 1),
	}
}

func (fw *FileWatcher) Watch(ctx context.Context) {
	go fw.runHandlers(ctx)

	interval := fw.Interval
	if interval == 0 {
		interval = defaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	_, err := fw.checkOnce()
	if err != nil {
		fw.errorCh <- err
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fileData, err := fw.checkOnce()
			if err != nil {
				select {
				case fw.errorCh <- err:
				default:
				}
			} else if fileData != nil {
				select {
				case fw.bytesCh <- fileData:
				default:
				}
			}
		}
	}
}
func (fw *FileWatcher) runHandlers(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-fw.errorCh:
			if fw.ErrorHandler != nil {
				fw.ErrorHandler(err)
			}
		case fileData := <-fw.bytesCh:
			if fw.FileChangedHandler != nil {
				fw.FileChangedHandler(fileData)
			}
		}
	}
}
func (fw *FileWatcher) checkOnce() ([]byte, error) {
	file, err := os.Stat(fw.filePath)
	if err != nil {
		return nil, err
	}
	newModTime := file.ModTime()
	newSize := file.Size()

	if fw.lastFileModTime.IsZero() {
		// prime state without emitting
		fileData, err := fw.getContent()
		if err != nil {
			return nil, err
		}
		hash := fw.getNewHash(fileData)

		fw.lastHashingTime = time.Now()
		fw.update(newModTime, newSize, hash)
		return nil, nil
	}

	//if metadata is the same and time that passed is not requires us to check the content, then just skip
	if newModTime == fw.lastFileModTime && newSize == fw.lastSize && !fw.hashingRequired() {
		log.Println("file stayed the same")
		return nil, nil
	}

	//make sure that content was actually changed
	fileData, err := fw.getContent()
	if err != nil {
		return nil, err
	}

	log.Println("hashing...")
	fw.lastHashingTime = time.Now()
	hash := fw.getNewHash(fileData)
	//check if the file was changed
	if bytes.Equal(hash, fw.lastHash) {
		return nil, nil
	}
	//the file was changed
	//update fields
	log.Println("file changed")
	fw.update(newModTime, newSize, hash)
	return fileData, nil
}
func (fw *FileWatcher) update(modTime time.Time, size int64, hash []byte) {
	fw.lastFileModTime = modTime
	fw.lastSize = size
	fw.lastHash = hash
}

func (fw *FileWatcher) getContent() ([]byte, error) {
	file, err := os.ReadFile(fw.filePath)
	if err != nil {
		return nil, err
	}
	return file, nil
}
func (fw *FileWatcher) getNewHash(file []byte) []byte {
	sum := sha256.Sum256(file)
	return sum[:]
}
func (fw *FileWatcher) hashingRequired() bool {
	now := time.Now()
	elapsed := now.Sub(fw.lastHashingTime)
	return elapsed > maxDurWithoutHashing
}
