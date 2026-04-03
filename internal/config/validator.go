package config

import "errors"

func validate(cfg ReverseProxyConfig) error {
	listenerAddresses := make(map[string]struct{}, len(cfg.Listeners))

	for _, l := range cfg.Listeners {
		_, exist := listenerAddresses[l.Listen]
		if exist {
			return errors.New("duplicate listener address: " + l.Listen)
		}

		// TLS validation.
		if l.TLS.Enabled {
			hasDefault := l.TLS.DefaultCertFile != "" && l.TLS.DefaultKeyFile != ""
			if !hasDefault && len(l.TLS.Certificates) == 0 {
				return errors.New("TLS enabled for listener " + l.Listen + ": provide certificates or default cert/key")
			}

			if (l.TLS.DefaultCertFile == "") != (l.TLS.DefaultKeyFile == "") {
				return errors.New("default TLS cert and key must both be set or both be empty")
			}

			certHosts := make(map[string]struct{}, len(l.TLS.Certificates))
			for _, c := range l.TLS.Certificates {
				if c.Hostname == "" {
					return errors.New("hostname must be set for TLS certificate entry in listener " + l.Listen)
				}
				if (c.CertFile == "") != (c.KeyFile == "") {
					return errors.New("TLS cert and key files must both be set or both be empty for hostname: " + c.Hostname)
				}
				if (len(c.CertData) == 0) != (len(c.KeyData) == 0) {
					return errors.New("TLS cert and key data must both be set or both be empty for hostname: " + c.Hostname)
				}
				if c.CertFile == "" && len(c.CertData) == 0 {
					return errors.New("TLS certificate entry must have cert_file/key_file or cert_data/key_data for hostname: " + c.Hostname)
				}
				certHosts[c.Hostname] = struct{}{}
			}

			if !hasDefault {
				for _, r := range l.Routes {
					if r.Match.Host == "" {
						return errors.New("TLS enabled for listener " + l.Listen + ": route without host requires default cert/key")
					}
					if _, ok := certHosts[r.Match.Host]; !ok {
						return errors.New("TLS enabled for listener " + l.Listen + ": missing certificate for route host " + r.Match.Host)
					}
				}
			}
		}

		// Rate limit validation.
		if l.RateLimit.Enabled {
			if l.RateLimit.ClientTTL < 0 {
				return errors.New("invalid rate limit config for listener " + l.Listen + ": client_ttl must be >= 0")
			}

			if l.RateLimit.Requests <= 0 || l.RateLimit.Window <= 0 {
				return errors.New("invalid rate limit config for listener " + l.Listen + ": requests and window must be > 0 when enabled")
			}
		}

		// Route validation.
		for _, r := range l.Routes {
			if r.Upstream == "" {
				return errors.New("upstream must be set for all routes in listener " + l.Listen)
			}
			if r.Match.Host == "" && r.Match.PathPrefix == "" {
				return errors.New("each route must have at least a host or path_prefix match in listener " + l.Listen)
			}
		}

		listenerAddresses[l.Listen] = struct{}{}
	}
	return nil
}
