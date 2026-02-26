package config

import "errors"

func validate(cfg ReverseProxyConfig) error {
	listenerAddresses := make(map[string]struct{}, len(cfg.Listeners))

	for _, l := range cfg.Listeners {
		_, exist := listenerAddresses[l.Listen]
		if exist {
			return errors.New("duplicate listener address: " + l.Listen)
		}

		// tls validation
		if l.Tls.Enabled {
			hasDefault := l.Tls.DefaultCertFile != "" && l.Tls.DefaultKeyFile != ""
			if !hasDefault && len(l.Tls.Certificates) == 0 {
				return errors.New("TLS enabled for listener " + l.Listen + ": provide certificates or default cert/key")
			}

			if (l.Tls.DefaultCertFile == "") != (l.Tls.DefaultKeyFile == "") {
				return errors.New("default TLS cert and key must both be set or both be empty")
			}

			certHosts := make(map[string]struct{}, len(l.Tls.Certificates))
			for _, c := range l.Tls.Certificates {
				if c.Hostname == "" {
					return errors.New("hostname must be set for TLS certificate entry in listener " + l.Listen)
				}
				if (c.CertFile == "") != (c.KeyFile == "") {
					return errors.New("TLS cert and key must both be set or both be empty for hostname: " + c.Hostname)
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

		//rate limit validation
		if l.RateLimit.Enabled {
			if l.RateLimit.ClientTTL < 0 {
				return errors.New("invalid rate limit config for listener " + l.Listen + ": client_ttl must be >= 0")
			}

			if l.RateLimit.Requests <= 0 || l.RateLimit.Window <= 0 {
				return errors.New("invalid rate limit config for listener " + l.Listen + ": requests and window must be > 0 when enabled")
			}
		}

		//route validation
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
