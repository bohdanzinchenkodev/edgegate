package config

import "testing"

func TestParseConfig_MalformedYAML_ReturnsError(t *testing.T) {
	file := []byte("listeners: [")

	_, err := ParseConfig(file)
	if err == nil {
		t.Fatalf("expected parse error")
	}
}

func TestParseConfig_InvalidType_ReturnsError(t *testing.T) {
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

func TestParseConfig_InvalidDuration_ReturnsError(t *testing.T) {
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

func TestParseConfig_MissingOptionalSections_ReturnsObjectWithZeroValues(t *testing.T) {
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
	if l.Tls.Enabled {
		t.Fatalf("expected tls.enabled to default to false")
	}
	if l.RateLimit.Enabled {
		t.Fatalf("expected rate_limit.enabled to default to false")
	}
}
