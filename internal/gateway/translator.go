package gateway

import (
	"fmt"

	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"edgegate/internal/config"
)

func Translate(gateway *gwv1.Gateway, httpRoutes []*gwv1.HTTPRoute) *config.ReverseProxyConfig {
	cfg := &config.ReverseProxyConfig{}

	for _, listener := range gateway.Spec.Listeners {
		l := config.Listener{
			Listen: fmt.Sprintf(":%d", listener.Port),
		}
		l.Routes = translateRoutes(listener, httpRoutes)
		cfg.Listeners = append(cfg.Listeners, l)
	}

	return cfg
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
