package config

import (
	"time"

	"github.com/goccy/go-yaml"
)

type ReverseProxyConfig struct {
	Listeners []Listener `yaml:"listeners"`
}
type Listener struct {
	Listen    string          `yaml:"listen"`
	Routes    []Route         `yaml:"routes"`
	RateLimit rateLimitPolicy `yaml:"rate_limit"`
	TLS       TLSConfig       `yaml:"tls"`
}
type Route struct {
	Match    Match  `yaml:"match"`
	Upstream string `yaml:"upstream"`
}
type Match struct {
	PathPrefix string `yaml:"path_prefix"`
	Host       string `yaml:"host"`
}
type TLSConfig struct {
	Enabled         bool        `yaml:"enabled"`
	Certificates    []CertEntry `yaml:"certificates"`
	DefaultCertFile string      `yaml:"default_cert_file"`
	DefaultKeyFile  string      `yaml:"default_key_file"`
}
type CertEntry struct {
	Hostname string `yaml:"hostname"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}
type rateLimitPolicy struct {
	Enabled bool `yaml:"enabled"`

	// Allow N requests per window (classic "10 req / 1s")
	Requests int           `yaml:"requests"` // e.g. 10
	Window   time.Duration `yaml:"window"`   // e.g. 1s, 1m

	// How long to keep an idle client in memory (TTL)
	ClientTTL time.Duration `yaml:"client_ttl"` // e.g. 5m

	// Trust forwarded headers only from these CIDRs
	TrustedProxies []string `yaml:"trusted_proxies"`
}

func ParseConfig(file []byte) (*ReverseProxyConfig, error) {
	cfg := &ReverseProxyConfig{}
	err := yaml.Unmarshal(file, cfg)
	if err != nil {
		return nil, err
	}
	err = validate(*cfg)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}
