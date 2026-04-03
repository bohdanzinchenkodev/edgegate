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

func TestCompileTlsManager_CertDataBuildEntries(t *testing.T) {
	certPEM, keyPEM := mustGenerateSelfSignedPEM(t, "app.example.com")

	l := config.Listener{}
	l.TLS.Enabled = true
	l.TLS.Certificates = []config.CertEntry{
		{Hostname: "app.example.com", CertData: certPEM, KeyData: keyPEM},
	}

	tm := compileTLSManager(l)
	if tm == nil {
		t.Fatalf("expected tls manager")
	}
	entry, ok := tm.certs["app.example.com"]
	if !ok {
		t.Fatalf("expected cert entry for app.example.com")
	}
	if len(entry.certData) == 0 || len(entry.keyData) == 0 {
		t.Fatalf("expected certData and keyData to be populated")
	}
}

func TestTlsManagerReload_FromCertData(t *testing.T) {
	certPEM, keyPEM := mustGenerateSelfSignedPEM(t, "app.example.com")

	tm := &tlsManager{certs: map[string]certEntry{
		"app.example.com": {certData: certPEM, keyData: keyPEM},
	}}

	err := tm.Reload(nil)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}

	cert, err := tm.GetCertificate(&tls.ClientHelloInfo{ServerName: "app.example.com"})
	if err != nil || cert == nil {
		t.Fatalf("expected cert from data, got err=%v", err)
	}
}

func TestTlsManagerReload_InvalidCertDataReturnsError(t *testing.T) {
	tm := &tlsManager{certs: map[string]certEntry{
		"app.example.com": {certData: []byte("bad"), keyData: []byte("bad")},
	}}

	err := tm.Reload(nil)
	if err == nil {
		t.Fatalf("expected error for invalid cert data")
	}
}

func TestTlsManagerEqual_MatchingCertData(t *testing.T) {
	data := []byte("cert-pem")
	a := &tlsManager{certs: map[string]certEntry{
		"app.example.com": {certData: data, keyData: data},
	}}
	b := &tlsManager{certs: map[string]certEntry{
		"app.example.com": {certData: data, keyData: data},
	}}
	c := &tlsManager{certs: map[string]certEntry{
		"app.example.com": {certData: []byte("other"), keyData: data},
	}}

	if !a.Equal(b) {
		t.Fatalf("expected equal tls managers with same cert data")
	}
	if a.Equal(c) {
		t.Fatalf("expected tls managers with different cert data to be not equal")
	}
}

func mustGenerateSelfSignedPEM(t *testing.T, commonName string) ([]byte, []byte) {
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

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	return certPEM, keyPEM
}

func mustWriteSelfSignedPair(t *testing.T, dir, commonName string) (string, string) {
	t.Helper()

	certPEM, keyPEM := mustGenerateSelfSignedPEM(t, commonName)

	certPath := filepath.Join(dir, commonName+".pem")
	keyPath := filepath.Join(dir, commonName+".key")

	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert file: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	return certPath, keyPath
}
