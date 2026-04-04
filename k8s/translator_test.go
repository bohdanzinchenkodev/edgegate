package k8s

import (
	"testing"

	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
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
