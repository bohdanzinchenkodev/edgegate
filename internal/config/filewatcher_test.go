package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeClock struct {
	currentTime time.Time
}

func (fc *fakeClock) Now() time.Time {
	return fc.currentTime
}

func (fc *fakeClock) Add(d time.Duration) {
	fc.currentTime = fc.currentTime.Add(d)
}

type fakeWatcherTicker struct {
	ch chan time.Time
}

func newFakeWatcherTicker() *fakeWatcherTicker {
	return &fakeWatcherTicker{ch: make(chan time.Time, 1)}
}

func (ft *fakeWatcherTicker) Chan() <-chan time.Time {
	return ft.ch
}

func (ft *fakeWatcherTicker) Stop() {}

func (ft *fakeWatcherTicker) Tick() {
	ft.ch <- time.Time{}
}

func TestFileWatcherCheckOnce_FirstRunReturnsFileBytesAndInitializesState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edgegate.yaml")
	content := []byte("listeners:\n  - listen: \":8080\"\n")

	err := os.WriteFile(path, content, 0o644)
	if err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	fw := NewFileWatcher(path)
	got, err := fw.checkOnce()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if !bytes.Equal(got, content) {
		t.Fatalf("expected first check to return file bytes")
	}

	if fw.lastFileModTime.IsZero() {
		t.Fatalf("expected lastFileModTime to be initialized")
	}
	if fw.lastSize != int64(len(content)) {
		t.Fatalf("expected lastSize=%d, got %d", len(content), fw.lastSize)
	}
	if fw.lastHashingTime.IsZero() {
		t.Fatalf("expected lastHashingTime to be initialized")
	}

	sum := sha256.Sum256(content)
	if !bytes.Equal(fw.lastHash, sum[:]) {
		t.Fatalf("expected lastHash to match file content hash")
	}
}

func TestFileWatcherCheckOnce_UnchangedMetadataAndRecentHashingReturnsNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edgegate.yaml")
	content := []byte("listeners:\n  - listen: \":8080\"\n")

	err := os.WriteFile(path, content, 0o644)
	if err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	fw := NewFileWatcher(path)
	fw.clock = &fakeClock{currentTime: time.Now()}

	_, err = fw.checkOnce()
	if err != nil {
		t.Fatalf("prime watcher state: %v", err)
	}

	got, err := fw.checkOnce()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil bytes when file metadata unchanged and hashing not required")
	}
}

func TestFileWatcherCheckOnce_MetadataChangedReturnsBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edgegate.yaml")
	content := []byte("listeners:\n  - listen: \":8080\"\n")
	updated := []byte("listeners:\n  - listen: \":9090\"\n")

	err := os.WriteFile(path, content, 0o644)
	if err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	fw := NewFileWatcher(path)
	_, err = fw.checkOnce()
	if err != nil {
		t.Fatalf("prime watcher state: %v", err)
	}

	err = os.WriteFile(path, updated, 0o644)
	if err != nil {
		t.Fatalf("update temp file: %v", err)
	}

	got, err := fw.checkOnce()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !bytes.Equal(got, updated) {
		t.Fatalf("expected updated bytes when metadata changed")
	}
}

func TestFileWatcherCheckOnce_ContentChangedWithSameMetadataReturnsBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edgegate.yaml")
	content := []byte("abc")
	updated := []byte("xyz")

	err := os.WriteFile(path, content, 0o644)
	if err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	fc := &fakeClock{currentTime: time.Now()}
	fw := NewFileWatcher(path)
	fw.clock = fc
	fw.MaxDurWithoutHashing = time.Second

	_, err = fw.checkOnce()
	if err != nil {
		t.Fatalf("prime watcher state: %v", err)
	}

	oldModTime := fw.lastFileModTime

	err = os.WriteFile(path, updated, 0o644)
	if err != nil {
		t.Fatalf("update temp file: %v", err)
	}
	err = os.Chtimes(path, oldModTime, oldModTime)
	if err != nil {
		t.Fatalf("set file times: %v", err)
	}

	fc.Add(2 * time.Second)

	got, err := fw.checkOnce()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !bytes.Equal(got, updated) {
		t.Fatalf("expected updated bytes when content changed")
	}
}

func TestFileWatcherCheckOnce_ExpiredHashWindowChecksContentEvenWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edgegate.yaml")
	content := []byte("listeners:\n  - listen: \":8080\"\n")

	err := os.WriteFile(path, content, 0o644)
	if err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	fc := &fakeClock{currentTime: time.Now()}
	fw := NewFileWatcher(path)
	fw.clock = fc
	fw.MaxDurWithoutHashing = time.Second

	_, err = fw.checkOnce()
	if err != nil {
		t.Fatalf("prime watcher state: %v", err)
	}

	prevHashTime := fw.lastHashingTime
	fc.Add(2 * time.Second)

	got, err := fw.checkOnce()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil bytes when content is unchanged")
	}
	if !fw.lastHashingTime.After(prevHashTime) {
		t.Fatalf("expected lastHashingTime to be updated after forced content check")
	}
}

func TestFileWatcherWatch_ReturnsBytesOnInitAndOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edgegate.yaml")
	initial := []byte("listeners:\n  - listen: \":8080\"\n")
	updated := []byte("listeners:\n  - listen: \":9090\"\n")

	err := os.WriteFile(path, initial, 0o644)
	if err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	changes := make(chan []byte, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fw := NewFileWatcher(path)
	ft := newFakeWatcherTicker()
	fw.newTicker = func(_ time.Duration) watcherTicker { return ft }
	fw.ReturnBytesOnInit = true
	fw.FileChangedHandler = func(file []byte) {
		changes <- append([]byte(nil), file...)
	}

	go fw.Watch(ctx)

	select {
	case got := <-changes:
		if !bytes.Equal(got, initial) {
			t.Fatalf("expected init bytes, got %q", string(got))
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatalf("timed out waiting for initial callback")
	}

	err = os.WriteFile(path, updated, 0o644)
	if err != nil {
		t.Fatalf("update temp file: %v", err)
	}
	ft.Tick()

	select {
	case got := <-changes:
		if !bytes.Equal(got, updated) {
			t.Fatalf("expected updated bytes, got %q", string(got))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for change callback")
	}
}

func TestFileWatcherWatch_MissingFileCallsErrorHandler(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.yaml")

	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fw := NewFileWatcher(path)
	ft := newFakeWatcherTicker()
	fw.newTicker = func(_ time.Duration) watcherTicker { return ft }
	fw.ErrorHandler = func(err error) {
		errCh <- err
	}

	go fw.Watch(ctx)

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("expected non-nil error")
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatalf("timed out waiting for error callback")
	}
}
