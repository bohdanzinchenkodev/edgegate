package egproxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"edgegate/internal/config"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompileTlsManager_DisabledReturnsNil(t *testing.T) {
	l := config.Listener{}

	tm := compileTLSManager(l)
	if tm != nil {
		t.Fatalf("expected nil tls manager when TLS is disabled")
	}
}

func TestCompileTlsManager_DefaultCertBuildsDefaultEntry(t *testing.T) {
	l := config.Listener{}
	l.TLS.Enabled = true
	l.TLS.DefaultCertFile = "./default.pem"
	l.TLS.DefaultKeyFile = "./default.key"

	tm := compileTLSManager(l)
	if tm == nil {
		t.Fatalf("expected tls manager")
	}
	entry, ok := tm.certs[DefaultCertKey]
	if !ok {
		t.Fatalf("expected default cert entry")
	}
	if entry.cert != "./default.pem" || entry.key != "./default.key" {
		t.Fatalf("unexpected default cert entry: %+v", entry)
	}
}

func TestCompileTlsManager_HostCertificatesBuildEntries(t *testing.T) {
	l := config.Listener{}
	l.TLS.Enabled = true
	l.TLS.Certificates = []config.CertEntry{
		{Hostname: "api.example.com", CertFile: "./api.pem", KeyFile: "./api.key"},
		{Hostname: "www.example.com", CertFile: "./www.pem", KeyFile: "./www.key"},
	}

	tm := compileTLSManager(l)
	if tm == nil {
		t.Fatalf("expected tls manager")
	}
	if len(tm.certs) != 2 {
		t.Fatalf("expected 2 cert entries, got %d", len(tm.certs))
	}
	if tm.certs["api.example.com"].cert != "./api.pem" {
		t.Fatalf("expected api cert path to match")
	}
	if tm.certs["www.example.com"].key != "./www.key" {
		t.Fatalf("expected www key path to match")
	}
}

func TestTlsManagerReloadAndGetCertificate_HostAndFallback(t *testing.T) {
	dir := t.TempDir()
	apiCert, apiKey := mustWriteSelfSignedPair(t, dir, "api.example.com")
	defaultCert, defaultKey := mustWriteSelfSignedPair(t, dir, "default")

	tm := &tlsManager{certs: map[string]certEntry{
		"api.example.com": {cert: apiCert, key: apiKey},
		DefaultCertKey:    {cert: defaultCert, key: defaultKey},
	}}

	err := tm.Reload(nil)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}

	cert, err := tm.GetCertificate(&tls.ClientHelloInfo{ServerName: "api.example.com"})
	if err != nil || cert == nil {
		t.Fatalf("expected host cert, got err=%v", err)
	}

	fallback, err := tm.GetCertificate(&tls.ClientHelloInfo{ServerName: "unknown.example.com"})
	if err != nil || fallback == nil {
		t.Fatalf("expected default fallback cert, got err=%v", err)
	}
}

func TestTlsManagerGetCertificate_NoMatchAndNoDefaultReturnsError(t *testing.T) {
	dir := t.TempDir()
	apiCert, apiKey := mustWriteSelfSignedPair(t, dir, "api.example.com")

	tm := &tlsManager{certs: map[string]certEntry{
		"api.example.com": {cert: apiCert, key: apiKey},
	}}

	err := tm.Reload(nil)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}

	cert, err := tm.GetCertificate(&tls.ClientHelloInfo{ServerName: "unknown.example.com"})
	if err == nil {
		t.Fatalf("expected error when no matching/default cert, got cert=%v", cert)
	}
}

func TestTlsManagerReload_InvalidEntryDoesNotSwapLoaded(t *testing.T) {
	dir := t.TempDir()
	goodCert, goodKey := mustWriteSelfSignedPair(t, dir, "api.example.com")

	tm := &tlsManager{certs: map[string]certEntry{
		"api.example.com": {cert: goodCert, key: goodKey},
	}}
	if err := tm.Reload(nil); err != nil {
		t.Fatalf("initial reload failed: %v", err)
	}

	before, err := tm.GetCertificate(&tls.ClientHelloInfo{ServerName: "api.example.com"})
	if err != nil || before == nil {
		t.Fatalf("expected existing cert before invalid reload, err=%v", err)
	}

	badEntries := map[string]certEntry{
		"api.example.com": {cert: filepath.Join(dir, "missing.pem"), key: filepath.Join(dir, "missing.key")},
	}
	if err := tm.Reload(badEntries); err == nil {
		t.Fatalf("expected reload error for missing cert files")
	}

	after, err := tm.GetCertificate(&tls.ClientHelloInfo{ServerName: "api.example.com"})
	if err != nil || after == nil {
		t.Fatalf("expected old cert to remain loaded, err=%v", err)
	}
	if after != before {
		t.Fatalf("expected loaded cert pointer to remain unchanged after failed reload")
	}
}

func TestTlsManagerEqual_MatchingAndDifferentConfigs(t *testing.T) {
	a := &tlsManager{certs: map[string]certEntry{
		DefaultCertKey: {cert: "a.pem", key: "a.key"},
	}}
	b := &tlsManager{certs: map[string]certEntry{
		DefaultCertKey: {cert: "a.pem", key: "a.key"},
	}}
	c := &tlsManager{certs: map[string]certEntry{
		DefaultCertKey: {cert: "b.pem", key: "b.key"},
	}}

	if !a.Equal(b) {
		t.Fatalf("expected equal tls managers")
	}
	if a.Equal(c) {
		t.Fatalf("expected tls managers with different certs to be not equal")
	}
	if a.Equal(nil) {
		t.Fatalf("expected non-nil manager to not equal nil")
	}
}

func mustWriteSelfSignedPair(t *testing.T, dir, commonName string) (string, string) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{commonName},
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	certPath := filepath.Join(dir, commonName+".pem")
	keyPath := filepath.Join(dir, commonName+".key")

	certOut, err := os.Create(certPath)
	if err != nil {
		t.Fatalf("create cert file: %v", err)
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("encode cert pem: %v", err)
	}

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyOut, err := os.Create(keyPath)
	if err != nil {
		t.Fatalf("create key file: %v", err)
	}
	defer keyOut.Close()
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		t.Fatalf("encode key pem: %v", err)
	}

	return certPath, keyPath
}
