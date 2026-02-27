package config

import "testing"

func TestValidate_DuplicateListenerAddresses_ReturnsError(t *testing.T) {
	cfg := ReverseProxyConfig{
		Listeners: []Listener{
			{
				Listen: ":8080",
				Routes: []Route{
					{
						Match:    Match{Host: "api.example.com"},
						Upstream: "http://127.0.0.1:9000",
					},
				},
			},
			{
				Listen: ":8080",
				Routes: []Route{
					{
						Match:    Match{Host: "other.example.com"},
						Upstream: "http://127.0.0.1:9001",
					},
				},
			},
		},
	}

	err := validate(cfg)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestValidate_TLSEnabledWithoutDefaultOrCertificates_ReturnsError(t *testing.T) {
	cfg := ReverseProxyConfig{
		Listeners: []Listener{
			{
				Listen: ":8080",
				Tls: TLSConfig{
					Enabled: true,
				},
				Routes: []Route{
					{
						Match:    Match{Host: "api.example.com"},
						Upstream: "http://127.0.0.1:9000",
					},
				},
			},
		},
	}

	err := validate(cfg)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestValidate_TLSEnabledWithoutDefaultAndMissingHostCert_ReturnsError(t *testing.T) {
	cfg := ReverseProxyConfig{
		Listeners: []Listener{
			{
				Listen: ":8080",
				Tls: TLSConfig{
					Enabled: true,
					Certificates: []CertEntry{
						{
							Hostname: "api.example.com",
							CertFile: "./api.pem",
							KeyFile:  "./api.key",
						},
					},
				},
				Routes: []Route{
					{
						Match:    Match{Host: "other.example.com"},
						Upstream: "http://127.0.0.1:9000",
					},
				},
			},
		},
	}

	err := validate(cfg)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestValidate_TLSEnabledWithDefaultCert_AllowsRouteWithoutHost(t *testing.T) {
	cfg := ReverseProxyConfig{
		Listeners: []Listener{
			{
				Listen: ":8080",
				Tls: TLSConfig{
					Enabled:         true,
					DefaultCertFile: "./default.pem",
					DefaultKeyFile:  "./default.key",
				},
				Routes: []Route{
					{
						Match:    Match{PathPrefix: "/api"},
						Upstream: "http://127.0.0.1:9000",
					},
				},
			},
		},
	}

	err := validate(cfg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidate_RateLimitEnabledWithInvalidNumbers_ReturnsError(t *testing.T) {
	cfg := ReverseProxyConfig{
		Listeners: []Listener{
			{
				Listen: ":8080",
				RateLimit: rateLimitPolicy{
					Enabled:  true,
					Requests: 0,
					Window:   0,
				},
				Routes: []Route{
					{
						Match:    Match{Host: "api.example.com"},
						Upstream: "http://127.0.0.1:9000",
					},
				},
			},
		},
	}

	err := validate(cfg)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestValidate_RouteWithoutHostAndPathPrefix_ReturnsError(t *testing.T) {
	cfg := ReverseProxyConfig{
		Listeners: []Listener{
			{
				Listen: ":8080",
				Routes: []Route{
					{
						Match:    Match{},
						Upstream: "http://127.0.0.1:9000",
					},
				},
			},
		},
	}

	err := validate(cfg)
	if err == nil {
		t.Fatalf("expected error")
	}
}
