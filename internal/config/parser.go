package config

import (
	"github.com/goccy/go-yaml"
)

type ReverseProxyConfig struct {
	Listeners []Listener `yaml:"listeners"`
}
type Listener struct {
	Listen string  `yaml:"listen"`
	Routes []route `yaml:"routes"`
}
type route struct {
	Match    match  `yaml:"match"`
	Upstream string `yaml:"upstream"`
}
type match struct {
	PathPrefix string `yaml:"path_prefix"`
	Host       string `yaml:"host"`
}

func ParseConfig(file []byte) (*ReverseProxyConfig, error) {
	cfg := &ReverseProxyConfig{}
	err := yaml.Unmarshal(file, cfg)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}
