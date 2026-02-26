package config

import "testing"

func TestParseConfig_TLSEnabledWithoutDefaultOrCertificates_ReturnsError(t *testing.T) {
	file := []byte(`listeners:
  - listen: ":8080"
    tls:
      enabled: true
    routes:
      - match:
          host: "api.example.com"
        upstream: "http://127.0.0.1:9000"
`)

	_, err := ParseConfig(file)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestParseConfig_TLSEnabledWithoutDefaultAndMissingHostCert_ReturnsError(t *testing.T) {
	file := []byte(`listeners:
  - listen: ":8080"
    tls:
      enabled: true
      certificates:
        - hostname: "api.example.com"
          cert_file: "./api.pem"
          key_file: "./api.key"
    routes:
      - match:
          host: "other.example.com"
        upstream: "http://127.0.0.1:9000"
`)

	_, err := ParseConfig(file)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestParseConfig_TLSEnabledWithDefaultCert_AllowsRouteWithoutHost(t *testing.T) {
	file := []byte(`listeners:
  - listen: ":8080"
    tls:
      enabled: true
      default_cert_file: "./default.pem"
      default_key_file: "./default.key"
    routes:
      - match:
          path_prefix: "/api"
        upstream: "http://127.0.0.1:9000"
`)

	_, err := ParseConfig(file)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
