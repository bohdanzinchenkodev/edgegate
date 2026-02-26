package egproxy

import (
	"crypto/tls"
	"fmt"
	"sync"

	"edgegate/internal/config"
)

const DefaultCertKey = "default"

type tlsManager struct {
	certs  map[string]certEntry // file paths, used for Equal() diffing
	mu     sync.RWMutex
	loaded map[string]*tls.Certificate // loaded certs, swapped on reload
}
type certEntry struct {
	key  string
	cert string
}

func compileTlsManager(l config.Listener) *tlsManager {
	if !l.Tls.Enabled {
		return nil
	}

	if len(l.Tls.Certificates) == 0 && l.Tls.DefaultCertFile == "" {
		return nil
	}
	if len(l.Tls.Certificates) > 0 {
		certs := make(map[string]certEntry, len(l.Tls.Certificates))
		for _, c := range l.Tls.Certificates {
			certs[c.Hostname] = certEntry{
				key:  c.KeyFile,
				cert: c.CertFile,
			}
		}
		return &tlsManager{certs: certs}
	}
	return &tlsManager{certs: map[string]certEntry{
		DefaultCertKey: {
			key:  l.Tls.DefaultKeyFile,
			cert: l.Tls.DefaultCertFile,
		},
	}}
}

// Reload loads certificates from the given entries and atomically swaps them in.
// If entries is nil, it reloads from the existing file paths.
// Existing TLS connections keep their old certs; new connections get the new ones.
func (t *tlsManager) Reload(entries map[string]certEntry) error {
	if entries == nil {
		entries = t.certs
	}
	loaded := make(map[string]*tls.Certificate, len(entries))
	for hostname, entry := range entries {
		cert, err := tls.LoadX509KeyPair(entry.cert, entry.key)
		if err != nil {
			return fmt.Errorf("loading cert for %s: %w", hostname, err)
		}
		loaded[hostname] = &cert
	}
	t.mu.Lock()
	t.certs = entries
	t.loaded = loaded
	t.mu.Unlock()
	return nil
}

// GetCertificate is the callback for tls.Config.GetCertificate.
// It looks up the cert by SNI hostname, falling back to the default cert.
func (t *tlsManager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if cert, ok := t.loaded[hello.ServerName]; ok {
		return cert, nil
	}
	if cert, ok := t.loaded[DefaultCertKey]; ok {
		return cert, nil
	}
	return nil, fmt.Errorf("no certificate for %s", hello.ServerName)
}

func (t *tlsManager) Equal(other *tlsManager) bool {
	if t == nil && other == nil {
		return true
	}
	if t == nil || other == nil {
		return false
	}
	if len(t.certs) != len(other.certs) {
		return false
	}
	for k, v := range t.certs {
		ov, ok := other.certs[k]
		if !ok || v != ov {
			return false
		}
	}
	return true
}
