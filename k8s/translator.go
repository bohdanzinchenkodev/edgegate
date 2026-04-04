package k8s

import (
	"fmt"

	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"edgegate/internal/config"
)

// TLSSecret holds the raw PEM data fetched from a Kubernetes Secret.
type TLSSecret struct {
	CertData []byte
	KeyData  []byte
}

func Translate(gateway *gwv1.Gateway, httpRoutes []*gwv1.HTTPRoute, tlsSecrets map[string]TLSSecret) *config.ReverseProxyConfig {
	cfg := &config.ReverseProxyConfig{}

	portGroups := make(map[int32][]gwv1.Listener)
	for _, listener := range gateway.Spec.Listeners {
		port := int32(listener.Port)
		portGroups[port] = append(portGroups[port], listener)
	}

	for port := range portGroups {
		l := config.Listener{
			Listen: fmt.Sprintf(":%d", port),
		}
		var allCerts []config.CertEntry
		for _, listener := range portGroups[port] {
			tlsCfg := translateTLS(gateway.Namespace, listener, tlsSecrets)
			if tlsCfg.Enabled {
				allCerts = append(allCerts, tlsCfg.Certificates...)
			}
			l.Routes = append(l.Routes, translateRoutes(listener, httpRoutes)...)
		}
		if len(allCerts) > 0 {
			l.TLS = config.TLSConfig{
				Enabled:      true,
				Certificates: allCerts,
			}
		}
		cfg.Listeners = append(cfg.Listeners, l)
	}

	return cfg
}

func translateTLS(gwNamespace string, listener gwv1.Listener, tlsSecrets map[string]TLSSecret) config.TLSConfig {
	if listener.TLS == nil || len(listener.TLS.CertificateRefs) == 0 {
		return config.TLSConfig{}
	}

	var certs []config.CertEntry
	for _, ref := range listener.TLS.CertificateRefs {
		ns := gwNamespace
		if ref.Namespace != nil {
			ns = string(*ref.Namespace)
		}
		key := ns + "/" + string(ref.Name)
		secret, ok := tlsSecrets[key]
		if !ok {
			continue
		}

		hostname := ""
		if listener.Hostname != nil {
			hostname = string(*listener.Hostname)
		}

		certs = append(certs, config.CertEntry{
			Hostname: hostname,
			CertData: secret.CertData,
			KeyData:  secret.KeyData,
		})
	}

	if len(certs) == 0 {
		return config.TLSConfig{}
	}

	return config.TLSConfig{
		Enabled:      true,
		Certificates: certs,
	}
}

func translateRoutes(listener gwv1.Listener, httpRoutes []*gwv1.HTTPRoute) []config.Route {
	var routes []config.Route

	for _, hr := range httpRoutes {
		if !routeMatchesListener(listener, hr) {
			continue
		}

		for _, rule := range hr.Spec.Rules {
			for _, backendRef := range rule.BackendRefs {
				upstream := backendToUpstream(hr.Namespace, backendRef.BackendObjectReference)
				if upstream == "" {
					continue
				}

				if len(rule.Matches) == 0 {
					for _, hostname := range hr.Spec.Hostnames {
						routes = append(routes, config.Route{
							Match:    config.Match{Host: string(hostname)},
							Upstream: upstream,
						})
					}
					if len(hr.Spec.Hostnames) == 0 {
						routes = append(routes, config.Route{Upstream: upstream})
					}
					continue
				}

				for _, match := range rule.Matches {
					for _, hostname := range hr.Spec.Hostnames {
						r := config.Route{
							Match:    config.Match{Host: string(hostname)},
							Upstream: upstream,
						}
						if match.Path != nil && match.Path.Value != nil {
							r.Match.PathPrefix = *match.Path.Value
						}
						routes = append(routes, r)
					}
					if len(hr.Spec.Hostnames) == 0 {
						r := config.Route{Upstream: upstream}
						if match.Path != nil && match.Path.Value != nil {
							r.Match.PathPrefix = *match.Path.Value
						}
						routes = append(routes, r)
					}
				}
			}
		}
	}

	return routes
}

func routeMatchesListener(listener gwv1.Listener, hr *gwv1.HTTPRoute) bool {
	for _, ref := range hr.Spec.ParentRefs {
		if ref.SectionName != nil && string(*ref.SectionName) == string(listener.Name) {
			return true
		}
		if ref.SectionName == nil {
			return true
		}
	}
	return false
}

func backendToUpstream(routeNamespace string, ref gwv1.BackendObjectReference) string {
	if ref.Group != nil && *ref.Group != "" {
		return ""
	}
	if ref.Kind != nil && *ref.Kind != "Service" {
		return ""
	}

	ns := routeNamespace
	if ref.Namespace != nil {
		ns = string(*ref.Namespace)
	}

	port := 80
	if ref.Port != nil {
		port = int(*ref.Port)
	}

	scheme := "http"
	if port == 443 {
		scheme = "https"
	}

	name := string(ref.Name)
	return fmt.Sprintf("%s://%s.%s.svc.cluster.local:%d", scheme, name, ns, port)
}
