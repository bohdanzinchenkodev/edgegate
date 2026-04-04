package k8s

import (
	"testing"

	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"edgegate/internal/config"
)

func sectionNamePtr(s string) *gwv1.SectionName {
	n := gwv1.SectionName(s)
	return &n
}

func hostnamePtr(s string) *gwv1.Hostname {
	n := gwv1.Hostname(s)
	return &n
}

func namespacePtr(s string) *gwv1.Namespace {
	n := gwv1.Namespace(s)
	return &n
}

func portNumberPtr(p int32) *gwv1.PortNumber {
	n := gwv1.PortNumber(p)
	return &n
}

func kindPtr(s string) *gwv1.Kind {
	n := gwv1.Kind(s)
	return &n
}

func groupPtr(s string) *gwv1.Group {
	n := gwv1.Group(s)
	return &n
}

func TestTranslate_SingleHTTPListenerProducesConfig(t *testing.T) {
	gw := &gwv1.Gateway{
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{
				{Name: "http", Port: 8080},
			},
		},
	}
	gw.Namespace = "default"

	hr := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw"}},
			},
			Hostnames: []gwv1.Hostname{"api.example.com"},
			Rules: []gwv1.HTTPRouteRule{
				{
					BackendRefs: []gwv1.HTTPBackendRef{
						{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{
							Name: "backend",
							Port: portNumberPtr(9000),
						}}},
					},
				},
			},
		},
	}
	hr.Namespace = "default"

	cfg := Translate(gw, []*gwv1.HTTPRoute{hr}, nil)

	if len(cfg.Listeners) != 1 {
		t.Fatalf("expected 1 listener, got %d", len(cfg.Listeners))
	}
	if cfg.Listeners[0].Listen != ":8080" {
		t.Fatalf("expected listen :8080, got %s", cfg.Listeners[0].Listen)
	}
	if len(cfg.Listeners[0].Routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(cfg.Listeners[0].Routes))
	}
	if cfg.Listeners[0].Routes[0].Match.Host != "api.example.com" {
		t.Fatalf("expected host api.example.com, got %s", cfg.Listeners[0].Routes[0].Match.Host)
	}
	if cfg.Listeners[0].Routes[0].Upstream != "http://backend.default.svc.cluster.local:9000" {
		t.Fatalf("unexpected upstream: %s", cfg.Listeners[0].Routes[0].Upstream)
	}
}

func TestTranslate_TLSListenerWithSecretEnablesTLS(t *testing.T) {
	gw := &gwv1.Gateway{
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{
				{
					Name:     "https",
					Port:     443,
					Hostname: hostnamePtr("secure.example.com"),
					TLS: &gwv1.ListenerTLSConfig{
						CertificateRefs: []gwv1.SecretObjectReference{
							{Name: "tls-secret"},
						},
					},
				},
			},
		},
	}
	gw.Namespace = "default"

	secrets := map[string]TLSSecret{
		"default/tls-secret": {
			CertData: []byte("cert-pem"),
			KeyData:  []byte("key-pem"),
		},
	}

	cfg := Translate(gw, nil, secrets)

	if !cfg.Listeners[0].TLS.Enabled {
		t.Fatalf("expected TLS enabled")
	}
	if len(cfg.Listeners[0].TLS.Certificates) != 1 {
		t.Fatalf("expected 1 cert, got %d", len(cfg.Listeners[0].TLS.Certificates))
	}
	cert := cfg.Listeners[0].TLS.Certificates[0]
	if cert.Hostname != "secure.example.com" {
		t.Fatalf("expected hostname secure.example.com, got %s", cert.Hostname)
	}
	if string(cert.CertData) != "cert-pem" {
		t.Fatalf("expected cert data cert-pem, got %s", string(cert.CertData))
	}
	if string(cert.KeyData) != "key-pem" {
		t.Fatalf("expected key data key-pem, got %s", string(cert.KeyData))
	}
}

func TestTranslate_SamePortListenersMergeIntoOne(t *testing.T) {
	gw := &gwv1.Gateway{
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{
				{
					Name:     "app-a",
					Port:     443,
					Hostname: hostnamePtr("app-a.example.com"),
					TLS: &gwv1.ListenerTLSConfig{
						CertificateRefs: []gwv1.SecretObjectReference{{Name: "cert-a"}},
					},
				},
				{
					Name:     "app-b",
					Port:     443,
					Hostname: hostnamePtr("app-b.example.com"),
					TLS: &gwv1.ListenerTLSConfig{
						CertificateRefs: []gwv1.SecretObjectReference{{Name: "cert-b"}},
					},
				},
			},
		},
	}
	gw.Namespace = "default"

	hrA := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", SectionName: sectionNamePtr("app-a")}},
			},
			Hostnames: []gwv1.Hostname{"app-a.example.com"},
			Rules: []gwv1.HTTPRouteRule{{
				BackendRefs: []gwv1.HTTPBackendRef{{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{
					Name: "svc-a", Port: portNumberPtr(80),
				}}}},
			}},
		},
	}
	hrA.Namespace = "default"

	hrB := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", SectionName: sectionNamePtr("app-b")}},
			},
			Hostnames: []gwv1.Hostname{"app-b.example.com"},
			Rules: []gwv1.HTTPRouteRule{{
				BackendRefs: []gwv1.HTTPBackendRef{{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{
					Name: "svc-b", Port: portNumberPtr(80),
				}}}},
			}},
		},
	}
	hrB.Namespace = "default"

	secrets := map[string]TLSSecret{
		"default/cert-a": {CertData: []byte("cert-a"), KeyData: []byte("key-a")},
		"default/cert-b": {CertData: []byte("cert-b"), KeyData: []byte("key-b")},
	}

	cfg := Translate(gw, []*gwv1.HTTPRoute{hrA, hrB}, secrets)

	if len(cfg.Listeners) != 1 {
		t.Fatalf("expected 1 merged listener, got %d", len(cfg.Listeners))
	}
	if cfg.Listeners[0].Listen != ":443" {
		t.Fatalf("expected listen :443, got %s", cfg.Listeners[0].Listen)
	}
	if len(cfg.Listeners[0].TLS.Certificates) != 2 {
		t.Fatalf("expected 2 certs, got %d", len(cfg.Listeners[0].TLS.Certificates))
	}
	if len(cfg.Listeners[0].Routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(cfg.Listeners[0].Routes))
	}
	if cfg.Listeners[0].Routes[0].Match.Host != "app-a.example.com" {
		t.Fatalf("expected first route host app-a.example.com, got %s", cfg.Listeners[0].Routes[0].Match.Host)
	}
	if cfg.Listeners[0].Routes[1].Match.Host != "app-b.example.com" {
		t.Fatalf("expected second route host app-b.example.com, got %s", cfg.Listeners[0].Routes[1].Match.Host)
	}
}

func TestTranslate_MissingSecretProducesNoTLS(t *testing.T) {
	gw := &gwv1.Gateway{
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{
				{
					Name: "https",
					Port: 443,
					TLS: &gwv1.ListenerTLSConfig{
						CertificateRefs: []gwv1.SecretObjectReference{
							{Name: "missing-secret"},
						},
					},
				},
			},
		},
	}
	gw.Namespace = "default"

	cfg := Translate(gw, nil, map[string]TLSSecret{})

	if cfg.Listeners[0].TLS.Enabled {
		t.Fatalf("expected TLS disabled when secret is missing")
	}
}

func TestTranslateTLS_NilTLSReturnsEmpty(t *testing.T) {
	listener := gwv1.Listener{Name: "http", Port: 8080}

	tlsCfg := translateTLS("default", listener, nil)

	if tlsCfg.Enabled {
		t.Fatalf("expected TLS disabled for listener without TLS")
	}
}

func TestTranslateTLS_CertRefFromDifferentNamespace(t *testing.T) {
	listener := gwv1.Listener{
		Name:     "https",
		Port:     443,
		Hostname: hostnamePtr("app.example.com"),
		TLS: &gwv1.ListenerTLSConfig{
			CertificateRefs: []gwv1.SecretObjectReference{
				{Name: "cross-ns-secret", Namespace: namespacePtr("cert-store")},
			},
		},
	}

	secrets := map[string]TLSSecret{
		"cert-store/cross-ns-secret": {CertData: []byte("cert"), KeyData: []byte("key")},
	}

	tlsCfg := translateTLS("default", listener, secrets)

	if !tlsCfg.Enabled {
		t.Fatalf("expected TLS enabled for cross-namespace cert ref")
	}
	if tlsCfg.Certificates[0].Hostname != "app.example.com" {
		t.Fatalf("expected hostname app.example.com, got %s", tlsCfg.Certificates[0].Hostname)
	}
}

func TestTranslateTLS_NoHostnameOnListenerSetsEmptyHostname(t *testing.T) {
	listener := gwv1.Listener{
		Name: "https",
		Port: 443,
		TLS: &gwv1.ListenerTLSConfig{
			CertificateRefs: []gwv1.SecretObjectReference{
				{Name: "my-secret"},
			},
		},
	}

	secrets := map[string]TLSSecret{
		"default/my-secret": {CertData: []byte("c"), KeyData: []byte("k")},
	}

	tlsCfg := translateTLS("default", listener, secrets)

	if tlsCfg.Certificates[0].Hostname != "" {
		t.Fatalf("expected empty hostname, got %s", tlsCfg.Certificates[0].Hostname)
	}
}

func TestTranslateRoutes_MatchesBySectionName(t *testing.T) {
	listener := gwv1.Listener{Name: "http"}

	hr := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{
					{Name: "gw", SectionName: sectionNamePtr("http")},
				},
			},
			Hostnames: []gwv1.Hostname{"api.example.com"},
			Rules: []gwv1.HTTPRouteRule{
				{
					BackendRefs: []gwv1.HTTPBackendRef{
						{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{
							Name: "svc", Port: portNumberPtr(80),
						}}},
					},
				},
			},
		},
	}
	hr.Namespace = "default"

	routes := translateRoutes(listener, []*gwv1.HTTPRoute{hr})

	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Match.Host != "api.example.com" {
		t.Fatalf("expected host api.example.com, got %s", routes[0].Match.Host)
	}
}

func TestTranslateRoutes_WrongSectionNameSkipsRoute(t *testing.T) {
	listener := gwv1.Listener{Name: "http"}

	hr := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{
					{Name: "gw", SectionName: sectionNamePtr("grpc")},
				},
			},
			Hostnames: []gwv1.Hostname{"api.example.com"},
			Rules: []gwv1.HTTPRouteRule{
				{
					BackendRefs: []gwv1.HTTPBackendRef{
						{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{
							Name: "svc", Port: portNumberPtr(80),
						}}},
					},
				},
			},
		},
	}
	hr.Namespace = "default"

	routes := translateRoutes(listener, []*gwv1.HTTPRoute{hr})

	if len(routes) != 0 {
		t.Fatalf("expected 0 routes for non-matching section name, got %d", len(routes))
	}
}

func TestTranslateRoutes_NilSectionNameMatchesAllListeners(t *testing.T) {
	listener := gwv1.Listener{Name: "any-listener"}

	hr := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{
					{Name: "gw"},
				},
			},
			Hostnames: []gwv1.Hostname{"api.example.com"},
			Rules: []gwv1.HTTPRouteRule{
				{
					BackendRefs: []gwv1.HTTPBackendRef{
						{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{
							Name: "svc", Port: portNumberPtr(80),
						}}},
					},
				},
			},
		},
	}
	hr.Namespace = "default"

	routes := translateRoutes(listener, []*gwv1.HTTPRoute{hr})

	if len(routes) != 1 {
		t.Fatalf("expected 1 route when section name is nil, got %d", len(routes))
	}
}

func TestTranslateRoutes_PathPrefixIncluded(t *testing.T) {
	listener := gwv1.Listener{Name: "http"}

	pathPrefix := "/api"
	hr := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw"}},
			},
			Hostnames: []gwv1.Hostname{"api.example.com"},
			Rules: []gwv1.HTTPRouteRule{
				{
					Matches: []gwv1.HTTPRouteMatch{
						{Path: &gwv1.HTTPPathMatch{Value: &pathPrefix}},
					},
					BackendRefs: []gwv1.HTTPBackendRef{
						{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{
							Name: "svc", Port: portNumberPtr(80),
						}}},
					},
				},
			},
		},
	}
	hr.Namespace = "default"

	routes := translateRoutes(listener, []*gwv1.HTTPRoute{hr})

	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Match.PathPrefix != "/api" {
		t.Fatalf("expected path prefix /api, got %s", routes[0].Match.PathPrefix)
	}
}

func TestTranslateRoutes_NoHostnamesProducesCatchAll(t *testing.T) {
	listener := gwv1.Listener{Name: "http"}

	hr := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw"}},
			},
			Rules: []gwv1.HTTPRouteRule{
				{
					BackendRefs: []gwv1.HTTPBackendRef{
						{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{
							Name: "svc", Port: portNumberPtr(80),
						}}},
					},
				},
			},
		},
	}
	hr.Namespace = "default"

	routes := translateRoutes(listener, []*gwv1.HTTPRoute{hr})

	if len(routes) != 1 {
		t.Fatalf("expected 1 catch-all route, got %d", len(routes))
	}
	if routes[0].Match.Host != "" {
		t.Fatalf("expected empty host for catch-all, got %s", routes[0].Match.Host)
	}
}

func TestRouteMatchesListener_MatchingSectionName(t *testing.T) {
	listener := gwv1.Listener{Name: "http"}
	hr := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{
					{Name: "gw", SectionName: sectionNamePtr("http")},
				},
			},
		},
	}

	if !routeMatchesListener(listener, hr) {
		t.Fatalf("expected route to match listener with same section name")
	}
}

func TestRouteMatchesListener_NilSectionNameMatchesAll(t *testing.T) {
	listener := gwv1.Listener{Name: "any"}
	hr := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw"}},
			},
		},
	}

	if !routeMatchesListener(listener, hr) {
		t.Fatalf("expected route with nil section name to match any listener")
	}
}

func TestRouteMatchesListener_WrongSectionNameNoMatch(t *testing.T) {
	listener := gwv1.Listener{Name: "http"}
	hr := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{
					{Name: "gw", SectionName: sectionNamePtr("grpc")},
				},
			},
		},
	}

	if routeMatchesListener(listener, hr) {
		t.Fatalf("expected route with different section name to not match")
	}
}

func TestBackendToUpstream_BasicService(t *testing.T) {
	ref := gwv1.BackendObjectReference{
		Name: "my-svc",
		Port: portNumberPtr(8080),
	}

	upstream := backendToUpstream("default", ref)

	if upstream != "http://my-svc.default.svc.cluster.local:8080" {
		t.Fatalf("unexpected upstream: %s", upstream)
	}
}

func TestBackendToUpstream_Port443UsesHTTPS(t *testing.T) {
	ref := gwv1.BackendObjectReference{
		Name: "my-svc",
		Port: portNumberPtr(443),
	}

	upstream := backendToUpstream("default", ref)

	if upstream != "https://my-svc.default.svc.cluster.local:443" {
		t.Fatalf("unexpected upstream: %s", upstream)
	}
}

func TestBackendToUpstream_DefaultPort(t *testing.T) {
	ref := gwv1.BackendObjectReference{Name: "my-svc"}

	upstream := backendToUpstream("default", ref)

	if upstream != "http://my-svc.default.svc.cluster.local:80" {
		t.Fatalf("unexpected upstream: %s", upstream)
	}
}

func TestBackendToUpstream_CustomNamespace(t *testing.T) {
	ref := gwv1.BackendObjectReference{
		Name:      "my-svc",
		Namespace: namespacePtr("other-ns"),
		Port:      portNumberPtr(8080),
	}

	upstream := backendToUpstream("default", ref)

	if upstream != "http://my-svc.other-ns.svc.cluster.local:8080" {
		t.Fatalf("unexpected upstream: %s", upstream)
	}
}

func TestBackendToUpstream_NonServiceKindReturnsEmpty(t *testing.T) {
	ref := gwv1.BackendObjectReference{
		Kind: kindPtr("ConfigMap"),
		Name: "my-cm",
		Port: portNumberPtr(8080),
	}

	upstream := backendToUpstream("default", ref)

	if upstream != "" {
		t.Fatalf("expected empty upstream for non-Service kind, got %s", upstream)
	}
}

func TestBackendToUpstream_NonEmptyGroupReturnsEmpty(t *testing.T) {
	ref := gwv1.BackendObjectReference{
		Group: groupPtr("custom.io"),
		Name:  "my-svc",
		Port:  portNumberPtr(8080),
	}

	upstream := backendToUpstream("default", ref)

	if upstream != "" {
		t.Fatalf("expected empty upstream for non-empty group, got %s", upstream)
	}
}

func findListener(listeners []config.Listener, listen string) *config.Listener {
	for i := range listeners {
		if listeners[i].Listen == listen {
			return &listeners[i]
		}
	}
	return nil
}

func TestTranslate_RealisticMultiListenerGateway(t *testing.T) {
	gw := &gwv1.Gateway{
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{
				{Name: "public-http", Port: 80},
				{
					Name:     "public-app",
					Port:     443,
					Hostname: hostnamePtr("app.example.com"),
					TLS: &gwv1.ListenerTLSConfig{
						CertificateRefs: []gwv1.SecretObjectReference{{Name: "app-tls-secret"}},
					},
				},
				{
					Name:     "public-api",
					Port:     443,
					Hostname: hostnamePtr("api.example.com"),
					TLS: &gwv1.ListenerTLSConfig{
						CertificateRefs: []gwv1.SecretObjectReference{{Name: "api-tls-secret"}},
					},
				},
				{
					Name:     "internal",
					Port:     8443,
					Hostname: hostnamePtr("admin.internal.io"),
					TLS: &gwv1.ListenerTLSConfig{
						CertificateRefs: []gwv1.SecretObjectReference{{Name: "admin-tls-secret"}},
					},
				},
			},
		},
	}
	gw.Namespace = "default"

	staticPath := "/static"
	wsPath := "/ws"
	v1Path := "/v1"
	v2Path := "/v2"
	metricsPath := "/metrics"
	rootPath := "/"

	appRoute := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "edgegate", SectionName: sectionNamePtr("public-app")}},
			},
			Hostnames: []gwv1.Hostname{"app.example.com"},
			Rules: []gwv1.HTTPRouteRule{
				{
					Matches:     []gwv1.HTTPRouteMatch{{Path: &gwv1.HTTPPathMatch{Value: &staticPath}}},
					BackendRefs: []gwv1.HTTPBackendRef{{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{Name: "cdn-svc", Port: portNumberPtr(80)}}}},
				},
				{
					Matches:     []gwv1.HTTPRouteMatch{{Path: &gwv1.HTTPPathMatch{Value: &wsPath}}},
					BackendRefs: []gwv1.HTTPBackendRef{{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{Name: "websocket-svc", Port: portNumberPtr(3000)}}}},
				},
				{
					BackendRefs: []gwv1.HTTPBackendRef{{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{Name: "frontend-svc", Port: portNumberPtr(80)}}}},
				},
			},
		},
	}
	appRoute.Namespace = "default"

	apiRoute := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "edgegate", SectionName: sectionNamePtr("public-api")}},
			},
			Hostnames: []gwv1.Hostname{"api.example.com"},
			Rules: []gwv1.HTTPRouteRule{
				{
					Matches:     []gwv1.HTTPRouteMatch{{Path: &gwv1.HTTPPathMatch{Value: &v1Path}}},
					BackendRefs: []gwv1.HTTPBackendRef{{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{Name: "api-v1-svc", Port: portNumberPtr(8080)}}}},
				},
				{
					Matches:     []gwv1.HTTPRouteMatch{{Path: &gwv1.HTTPPathMatch{Value: &v2Path}}},
					BackendRefs: []gwv1.HTTPBackendRef{{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{Name: "api-v2-svc", Port: portNumberPtr(8080)}}}},
				},
			},
		},
	}
	apiRoute.Namespace = "default"

	redirectRoute := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "edgegate", SectionName: sectionNamePtr("public-http")}},
			},
			Hostnames: []gwv1.Hostname{"app.example.com", "api.example.com"},
			Rules: []gwv1.HTTPRouteRule{
				{
					BackendRefs: []gwv1.HTTPBackendRef{{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{Name: "redirect-svc", Port: portNumberPtr(80)}}}},
				},
			},
		},
	}
	redirectRoute.Namespace = "default"

	adminRoute := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "edgegate", SectionName: sectionNamePtr("internal")}},
			},
			Hostnames: []gwv1.Hostname{"admin.internal.io"},
			Rules: []gwv1.HTTPRouteRule{
				{
					Matches:     []gwv1.HTTPRouteMatch{{Path: &gwv1.HTTPPathMatch{Value: &metricsPath}}},
					BackendRefs: []gwv1.HTTPBackendRef{{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{Name: "prometheus-svc", Port: portNumberPtr(9090)}}}},
				},
				{
					Matches:     []gwv1.HTTPRouteMatch{{Path: &gwv1.HTTPPathMatch{Value: &rootPath}}},
					BackendRefs: []gwv1.HTTPBackendRef{{BackendRef: gwv1.BackendRef{BackendObjectReference: gwv1.BackendObjectReference{Name: "admin-dashboard-svc", Port: portNumberPtr(3000)}}}},
				},
			},
		},
	}
	adminRoute.Namespace = "default"

	secrets := map[string]TLSSecret{
		"default/app-tls-secret":   {CertData: []byte("app-cert"), KeyData: []byte("app-key")},
		"default/api-tls-secret":   {CertData: []byte("api-cert"), KeyData: []byte("api-key")},
		"default/admin-tls-secret": {CertData: []byte("admin-cert"), KeyData: []byte("admin-key")},
	}

	routes := []*gwv1.HTTPRoute{appRoute, apiRoute, redirectRoute, adminRoute}
	cfg := Translate(gw, routes, secrets)

	// 3 listeners: :80, :443 (merged), :8443
	if len(cfg.Listeners) != 3 {
		t.Fatalf("expected 3 listeners, got %d", len(cfg.Listeners))
	}

	// --- :80 listener ---
	http := findListener(cfg.Listeners, ":80")
	if http == nil {
		t.Fatalf("listener :80 not found")
	}
	if http.TLS.Enabled {
		t.Fatalf("expected no TLS on :80")
	}
	if len(http.Routes) != 2 {
		t.Fatalf("expected 2 routes on :80, got %d", len(http.Routes))
	}
	if http.Routes[0].Match.Host != "app.example.com" {
		t.Fatalf("expected :80 route[0] host app.example.com, got %s", http.Routes[0].Match.Host)
	}
	if http.Routes[1].Match.Host != "api.example.com" {
		t.Fatalf("expected :80 route[1] host api.example.com, got %s", http.Routes[1].Match.Host)
	}
	if http.Routes[0].Upstream != "http://redirect-svc.default.svc.cluster.local:80" {
		t.Fatalf("expected :80 route[0] upstream redirect-svc, got %s", http.Routes[0].Upstream)
	}

	// --- :443 listener (merged from public-app + public-api) ---
	https := findListener(cfg.Listeners, ":443")
	if https == nil {
		t.Fatalf("listener :443 not found")
	}
	if !https.TLS.Enabled {
		t.Fatalf("expected TLS enabled on :443")
	}
	if len(https.TLS.Certificates) != 2 {
		t.Fatalf("expected 2 certs on :443, got %d", len(https.TLS.Certificates))
	}
	if https.TLS.Certificates[0].Hostname != "app.example.com" {
		t.Fatalf("expected cert[0] hostname app.example.com, got %s", https.TLS.Certificates[0].Hostname)
	}
	if https.TLS.Certificates[1].Hostname != "api.example.com" {
		t.Fatalf("expected cert[1] hostname api.example.com, got %s", https.TLS.Certificates[1].Hostname)
	}
	// 3 from app-route + 2 from api-route = 5
	if len(https.Routes) != 5 {
		t.Fatalf("expected 5 routes on :443, got %d", len(https.Routes))
	}
	if https.Routes[0].Match.Host != "app.example.com" || https.Routes[0].Match.PathPrefix != "/static" {
		t.Fatalf("expected :443 route[0] app.example.com /static, got %s %s", https.Routes[0].Match.Host, https.Routes[0].Match.PathPrefix)
	}
	if https.Routes[0].Upstream != "http://cdn-svc.default.svc.cluster.local:80" {
		t.Fatalf("expected :443 route[0] upstream cdn-svc, got %s", https.Routes[0].Upstream)
	}
	if https.Routes[1].Match.PathPrefix != "/ws" {
		t.Fatalf("expected :443 route[1] path /ws, got %s", https.Routes[1].Match.PathPrefix)
	}
	if https.Routes[2].Match.Host != "app.example.com" || https.Routes[2].Match.PathPrefix != "" {
		t.Fatalf("expected :443 route[2] app.example.com catch-all, got %s %s", https.Routes[2].Match.Host, https.Routes[2].Match.PathPrefix)
	}
	if https.Routes[2].Upstream != "http://frontend-svc.default.svc.cluster.local:80" {
		t.Fatalf("expected :443 route[2] upstream frontend-svc, got %s", https.Routes[2].Upstream)
	}
	if https.Routes[3].Match.Host != "api.example.com" || https.Routes[3].Match.PathPrefix != "/v1" {
		t.Fatalf("expected :443 route[3] api.example.com /v1, got %s %s", https.Routes[3].Match.Host, https.Routes[3].Match.PathPrefix)
	}
	if https.Routes[4].Match.PathPrefix != "/v2" {
		t.Fatalf("expected :443 route[4] path /v2, got %s", https.Routes[4].Match.PathPrefix)
	}

	// --- :8443 listener ---
	internal := findListener(cfg.Listeners, ":8443")
	if internal == nil {
		t.Fatalf("listener :8443 not found")
	}
	if !internal.TLS.Enabled {
		t.Fatalf("expected TLS enabled on :8443")
	}
	if len(internal.TLS.Certificates) != 1 {
		t.Fatalf("expected 1 cert on :8443, got %d", len(internal.TLS.Certificates))
	}
	if internal.TLS.Certificates[0].Hostname != "admin.internal.io" {
		t.Fatalf("expected cert hostname admin.internal.io, got %s", internal.TLS.Certificates[0].Hostname)
	}
	if len(internal.Routes) != 2 {
		t.Fatalf("expected 2 routes on :8443, got %d", len(internal.Routes))
	}
	if internal.Routes[0].Match.PathPrefix != "/metrics" {
		t.Fatalf("expected :8443 route[0] path /metrics, got %s", internal.Routes[0].Match.PathPrefix)
	}
	if internal.Routes[0].Upstream != "http://prometheus-svc.default.svc.cluster.local:9090" {
		t.Fatalf("expected :8443 route[0] upstream prometheus-svc, got %s", internal.Routes[0].Upstream)
	}
	if internal.Routes[1].Match.PathPrefix != "/" {
		t.Fatalf("expected :8443 route[1] path /, got %s", internal.Routes[1].Match.PathPrefix)
	}
	if internal.Routes[1].Upstream != "http://admin-dashboard-svc.default.svc.cluster.local:3000" {
		t.Fatalf("expected :8443 route[1] upstream admin-dashboard-svc, got %s", internal.Routes[1].Upstream)
	}
}
