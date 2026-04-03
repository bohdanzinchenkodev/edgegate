package config

import "testing"

func TestParseConfig_MalformedYAMLReturnsError(t *testing.T) {
	file := []byte("listeners: [")

	_, err := ParseConfig(file)
	if err == nil {
		t.Fatalf("expected parse error")
	}
}

func TestParseConfig_InvalidTypeReturnsError(t *testing.T) {
	file := []byte(`listeners:
  - listen: ":8080"
    rate_limit:
      enabled: true
      requests: "ten"
      window: 1s
    routes:
      - match:
          host: "api.example.com"
        upstream: "http://127.0.0.1:9000"
`)

	_, err := ParseConfig(file)
	if err == nil {
		t.Fatalf("expected parse error for invalid type")
	}
}

func TestParseConfig_InvalidDurationReturnsError(t *testing.T) {
	file := []byte(`listeners:
  - listen: ":8080"
    rate_limit:
      enabled: true
      requests: 10
      window: "not-a-duration"
    routes:
      - match:
          host: "api.example.com"
        upstream: "http://127.0.0.1:9000"
`)

	_, err := ParseConfig(file)
	if err == nil {
		t.Fatalf("expected parse error for invalid duration")
	}
}

func TestParseConfig_MissingOptionalSectionsReturnsObjectWithZeroValues(t *testing.T) {
	file := []byte(`listeners:
  - listen: ":8080"
    routes:
      - match:
          host: "api.example.com"
        upstream: "http://127.0.0.1:9000"
`)

	cfg, err := ParseConfig(file)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(cfg.Listeners) != 1 {
		t.Fatalf("expected one listener, got %d", len(cfg.Listeners))
	}

	l := cfg.Listeners[0]
	if l.TLS.Enabled {
		t.Fatalf("expected tls.enabled to default to false")
	}
	if l.RateLimit.Enabled {
		t.Fatalf("expected rate_limit.enabled to default to false")
	}
}

func TestParseConfig_TLSWithCertFilesParsesCertEntries(t *testing.T) {
	file := []byte(`listeners:
  - listen: ":443"
    tls:
      enabled: true
      certificates:
        - hostname: "app.example.com"
          cert_file: "./app.pem"
          key_file: "./app.key"
    routes:
      - match:
          host: "app.example.com"
        upstream: "http://127.0.0.1:9000"
`)

	cfg, err := ParseConfig(file)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	l := cfg.Listeners[0]
	if !l.TLS.Enabled {
		t.Fatalf("expected tls.enabled to be true")
	}
	if len(l.TLS.Certificates) != 1 {
		t.Fatalf("expected 1 certificate, got %d", len(l.TLS.Certificates))
	}
	c := l.TLS.Certificates[0]
	if c.Hostname != "app.example.com" {
		t.Fatalf("expected hostname app.example.com, got %s", c.Hostname)
	}
	if c.CertFile != "./app.pem" {
		t.Fatalf("expected cert_file ./app.pem, got %s", c.CertFile)
	}
	if c.KeyFile != "./app.key" {
		t.Fatalf("expected key_file ./app.key, got %s", c.KeyFile)
	}
}

func TestParseConfig_TLSWithCertDataParsesCertEntries(t *testing.T) {
	file := []byte("listeners:\n" +
		"  - listen: \":443\"\n" +
		"    tls:\n" +
		"      enabled: true\n" +
		"      certificates:\n" +
		"        - hostname: \"app.example.com\"\n" +
		"          cert_data:\n" +
		"            - 99\n" +
		"            - 101\n" +
		"            - 114\n" +
		"            - 116\n" +
		"          key_data:\n" +
		"            - 107\n" +
		"            - 101\n" +
		"            - 121\n" +
		"    routes:\n" +
		"      - match:\n" +
		"          host: \"app.example.com\"\n" +
		"        upstream: \"http://127.0.0.1:9000\"\n")

	cfg, err := ParseConfig(file)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	c := cfg.Listeners[0].TLS.Certificates[0]
	if string(c.CertData) != "cert" {
		t.Fatalf("expected cert_data 'cert', got %q", string(c.CertData))
	}
	if string(c.KeyData) != "key" {
		t.Fatalf("expected key_data 'key', got %q", string(c.KeyData))
	}
}

func TestParseConfig_TLSWithDefaultCertFilesParsesDefaults(t *testing.T) {
	file := []byte(`listeners:
  - listen: ":443"
    tls:
      enabled: true
      default_cert_file: "./default.pem"
      default_key_file: "./default.key"
    routes:
      - match:
          path_prefix: "/api"
        upstream: "http://127.0.0.1:9000"
`)

	cfg, err := ParseConfig(file)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	l := cfg.Listeners[0]
	if l.TLS.DefaultCertFile != "./default.pem" {
		t.Fatalf("expected default_cert_file ./default.pem, got %s", l.TLS.DefaultCertFile)
	}
	if l.TLS.DefaultKeyFile != "./default.key" {
		t.Fatalf("expected default_key_file ./default.key, got %s", l.TLS.DefaultKeyFile)
	}
}
