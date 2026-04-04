package egproxy

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"edgegate/internal/config"
)

// Helpers

func startIntegrationUpstream(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func startIntegrationUpstreamFunc(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func resetAndCleanup(t *testing.T) {
	t.Helper()
	ShutdownAll()
	resetEngineStateForTest()
}

func applyConfigAndWait(t *testing.T, cfg *config.ReverseProxyConfig, addrs ...string) {
	t.Helper()
	ApplyConfig(cfg)
	for _, addr := range addrs {
		waitForListener(t, addr)
	}
}

func waitForListener(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("listener %s did not become ready within 3s", addr)
}

func waitForListenerDown(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err != nil {
			return
		}
		conn.Close()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("listener %s did not shut down within 3s", addr)
}

func httpGetThroughProxy(t *testing.T, addr, path string) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + addr + path)
	if err != nil {
		t.Fatalf("HTTP GET http://%s%s failed: %v", addr, path, err)
	}
	return resp
}

func httpGetThroughProxyWithHost(t *testing.T, addr, host, path string) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", "http://"+addr+path, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Host = host
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTP GET http://%s%s (Host: %s) failed: %v", addr, path, host, err)
	}
	return resp
}

func httpsGetThroughProxy(t *testing.T, addr, serverName, path string) *http.Response {
	t.Helper()
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				ServerName:         serverName,
			},
		},
	}
	req, err := http.NewRequest("GET", "https://"+addr+path, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Host = serverName
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTPS GET https://%s%s failed: %v", addr, path, err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// HTTP Routing

func TestIntegrationRouting_SingleRoute(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	resetAndCleanup(t)
	defer resetAndCleanup(t)

	upstream := startIntegrationUpstream(t, "upstream-A")

	cfg := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":19001",
				Routes: []config.Route{
					{Match: config.Match{PathPrefix: "/"}, Upstream: upstream.URL},
				},
			},
		},
	}
	applyConfigAndWait(t, cfg, "localhost:19001")

	resp := httpGetThroughProxy(t, "localhost:19001", "/anything")
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if body != "upstream-A" {
		t.Fatalf("expected body %q, got %q", "upstream-A", body)
	}
}

func TestIntegrationRouting_MultipleRoutes(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	resetAndCleanup(t)
	defer resetAndCleanup(t)

	upstreamA := startIntegrationUpstream(t, "upstream-A")
	upstreamB := startIntegrationUpstream(t, "upstream-B")

	cfg := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":19002",
				Routes: []config.Route{
					{Match: config.Match{PathPrefix: "/api"}, Upstream: upstreamA.URL},
					{Match: config.Match{PathPrefix: "/"}, Upstream: upstreamB.URL},
				},
			},
		},
	}
	applyConfigAndWait(t, cfg, "localhost:19002")

	respA := httpGetThroughProxy(t, "localhost:19002", "/api/foo")
	bodyA := readBody(t, respA)
	if bodyA != "upstream-A" {
		t.Fatalf("expected body %q for /api/foo, got %q", "upstream-A", bodyA)
	}

	respB := httpGetThroughProxy(t, "localhost:19002", "/other")
	bodyB := readBody(t, respB)
	if bodyB != "upstream-B" {
		t.Fatalf("expected body %q for /other, got %q", "upstream-B", bodyB)
	}
}

func TestIntegrationRouting_HostBasedRouting(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	resetAndCleanup(t)
	defer resetAndCleanup(t)

	upstreamA := startIntegrationUpstream(t, "host-a")
	upstreamB := startIntegrationUpstream(t, "host-b")

	cfg := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":19003",
				Routes: []config.Route{
					{Match: config.Match{Host: "a.example.com", PathPrefix: "/"}, Upstream: upstreamA.URL},
					{Match: config.Match{Host: "b.example.com", PathPrefix: "/"}, Upstream: upstreamB.URL},
				},
			},
		},
	}
	applyConfigAndWait(t, cfg, "localhost:19003")

	respA := httpGetThroughProxyWithHost(t, "localhost:19003", "a.example.com", "/")
	bodyA := readBody(t, respA)
	if bodyA != "host-a" {
		t.Fatalf("expected body %q for host a.example.com, got %q", "host-a", bodyA)
	}

	respB := httpGetThroughProxyWithHost(t, "localhost:19003", "b.example.com", "/")
	bodyB := readBody(t, respB)
	if bodyB != "host-b" {
		t.Fatalf("expected body %q for host b.example.com, got %q", "host-b", bodyB)
	}
}

func TestIntegrationRouting_PathPrefixStripping(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	resetAndCleanup(t)
	defer resetAndCleanup(t)

	upstream := startIntegrationUpstreamFunc(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.URL.Path)
	})

	cfg := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":19004",
				Routes: []config.Route{
					{Match: config.Match{PathPrefix: "/api"}, Upstream: upstream.URL},
				},
			},
		},
	}
	applyConfigAndWait(t, cfg, "localhost:19004")

	resp := httpGetThroughProxy(t, "localhost:19004", "/api/users")
	body := readBody(t, resp)
	if body != "/users" {
		t.Fatalf("expected upstream to see %q, got %q", "/users", body)
	}
}

func TestIntegrationRouting_FirstMatchWins(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	resetAndCleanup(t)
	defer resetAndCleanup(t)

	upstreamFirst := startIntegrationUpstream(t, "first")
	upstreamSecond := startIntegrationUpstream(t, "second")

	cfg := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":19005",
				Routes: []config.Route{
					{Match: config.Match{PathPrefix: "/api"}, Upstream: upstreamFirst.URL},
					{Match: config.Match{PathPrefix: "/api"}, Upstream: upstreamSecond.URL},
				},
			},
		},
	}
	applyConfigAndWait(t, cfg, "localhost:19005")

	resp := httpGetThroughProxy(t, "localhost:19005", "/api/test")
	body := readBody(t, resp)
	if body != "first" {
		t.Fatalf("expected first-match-wins body %q, got %q", "first", body)
	}
}

func TestIntegrationRouting_NoMatchReturnsBadGateway(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	resetAndCleanup(t)
	defer resetAndCleanup(t)

	upstream := startIntegrationUpstream(t, "should-not-reach")

	cfg := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":19006",
				Routes: []config.Route{
					{Match: config.Match{Host: "specific.example.com", PathPrefix: "/"}, Upstream: upstream.URL},
				},
			},
		},
	}
	applyConfigAndWait(t, cfg, "localhost:19006")

	resp := httpGetThroughProxyWithHost(t, "localhost:19006", "wrong.example.com", "/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.StatusCode)
	}
}

// TLS

func TestIntegrationTLS_HTTPSWithCertData(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	resetAndCleanup(t)
	defer resetAndCleanup(t)

	upstream := startIntegrationUpstream(t, "tls-ok")
	certPEM, keyPEM := mustGenerateSelfSignedPEM(t, "localhost")

	cfg := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":19010",
				TLS: config.TLSConfig{
					Enabled: true,
					Certificates: []config.CertEntry{
						{Hostname: "localhost", CertData: certPEM, KeyData: keyPEM},
					},
				},
				Routes: []config.Route{
					{Match: config.Match{PathPrefix: "/"}, Upstream: upstream.URL},
				},
			},
		},
	}
	applyConfigAndWait(t, cfg, "localhost:19010")

	resp := httpsGetThroughProxy(t, "localhost:19010", "localhost", "/")
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if body != "tls-ok" {
		t.Fatalf("expected body %q, got %q", "tls-ok", body)
	}
}

func TestIntegrationTLS_SNIBasedCertSelection(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	resetAndCleanup(t)
	defer resetAndCleanup(t)

	upstreamAlpha := startIntegrationUpstream(t, "alpha")
	upstreamBeta := startIntegrationUpstream(t, "beta")
	alphaCert, alphaKey := mustGenerateSelfSignedPEM(t, "alpha.local")
	betaCert, betaKey := mustGenerateSelfSignedPEM(t, "beta.local")

	cfg := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":19011",
				TLS: config.TLSConfig{
					Enabled: true,
					Certificates: []config.CertEntry{
						{Hostname: "alpha.local", CertData: alphaCert, KeyData: alphaKey},
						{Hostname: "beta.local", CertData: betaCert, KeyData: betaKey},
					},
				},
				Routes: []config.Route{
					{Match: config.Match{Host: "alpha.local", PathPrefix: "/"}, Upstream: upstreamAlpha.URL},
					{Match: config.Match{Host: "beta.local", PathPrefix: "/"}, Upstream: upstreamBeta.URL},
				},
			},
		},
	}
	applyConfigAndWait(t, cfg, "localhost:19011")

	respAlpha := httpsGetThroughProxy(t, "localhost:19011", "alpha.local", "/")
	bodyAlpha := readBody(t, respAlpha)
	if bodyAlpha != "alpha" {
		t.Fatalf("expected body %q for alpha.local, got %q", "alpha", bodyAlpha)
	}
	if respAlpha.TLS == nil || len(respAlpha.TLS.PeerCertificates) == 0 {
		t.Fatalf("expected TLS peer certificates for alpha.local")
	}
	if respAlpha.TLS.PeerCertificates[0].Subject.CommonName != "alpha.local" {
		t.Fatalf("expected cert CN %q, got %q", "alpha.local", respAlpha.TLS.PeerCertificates[0].Subject.CommonName)
	}

	respBeta := httpsGetThroughProxy(t, "localhost:19011", "beta.local", "/")
	bodyBeta := readBody(t, respBeta)
	if bodyBeta != "beta" {
		t.Fatalf("expected body %q for beta.local, got %q", "beta", bodyBeta)
	}
	if respBeta.TLS.PeerCertificates[0].Subject.CommonName != "beta.local" {
		t.Fatalf("expected cert CN %q, got %q", "beta.local", respBeta.TLS.PeerCertificates[0].Subject.CommonName)
	}
}

// Rate Limiting

func TestIntegrationRateLimit_BlocksAfterExceedingLimit(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	resetAndCleanup(t)
	defer resetAndCleanup(t)

	upstream := startIntegrationUpstream(t, "ok")

	listener := config.Listener{
		Listen: ":19020",
		Routes: []config.Route{
			{Match: config.Match{PathPrefix: "/"}, Upstream: upstream.URL},
		},
	}
	listener.RateLimit.Enabled = true
	listener.RateLimit.Requests = 3
	listener.RateLimit.Window = 10 * time.Second
	listener.RateLimit.ClientTTL = 30 * time.Second
	cfg := &config.ReverseProxyConfig{Listeners: []config.Listener{listener}}
	applyConfigAndWait(t, cfg, "localhost:19020")

	for i := 0; i < 3; i++ {
		resp := httpGetThroughProxy(t, "localhost:19020", "/")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, resp.StatusCode)
		}
	}

	resp := httpGetThroughProxy(t, "localhost:19020", "/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("4th request: expected 429, got %d", resp.StatusCode)
	}
}

func TestIntegrationRateLimit_AllowsRequestsWithinLimit(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	resetAndCleanup(t)
	defer resetAndCleanup(t)

	upstream := startIntegrationUpstream(t, "ok")

	listener := config.Listener{
		Listen: ":19021",
		Routes: []config.Route{
			{Match: config.Match{PathPrefix: "/"}, Upstream: upstream.URL},
		},
	}
	listener.RateLimit.Enabled = true
	listener.RateLimit.Requests = 10
	listener.RateLimit.Window = 1 * time.Second
	listener.RateLimit.ClientTTL = 30 * time.Second
	cfg := &config.ReverseProxyConfig{Listeners: []config.Listener{listener}}
	applyConfigAndWait(t, cfg, "localhost:19021")

	for i := 0; i < 5; i++ {
		resp := httpGetThroughProxy(t, "localhost:19021", "/")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, resp.StatusCode)
		}
	}
}

// Hot Reload

func TestIntegrationHotReload_RouteChangeUpdatesTraffic(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	resetAndCleanup(t)
	defer resetAndCleanup(t)

	upstreamBefore := startIntegrationUpstream(t, "before")
	upstreamAfter := startIntegrationUpstream(t, "after")

	cfgV1 := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":19030",
				Routes: []config.Route{
					{Match: config.Match{PathPrefix: "/"}, Upstream: upstreamBefore.URL},
				},
			},
		},
	}
	applyConfigAndWait(t, cfgV1, "localhost:19030")

	resp := httpGetThroughProxy(t, "localhost:19030", "/")
	body := readBody(t, resp)
	if body != "before" {
		t.Fatalf("expected body %q before reload, got %q", "before", body)
	}

	cfgV2 := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":19030",
				Routes: []config.Route{
					{Match: config.Match{PathPrefix: "/"}, Upstream: upstreamAfter.URL},
				},
			},
		},
	}
	ApplyConfig(cfgV2)

	resp = httpGetThroughProxy(t, "localhost:19030", "/")
	body = readBody(t, resp)
	if body != "after" {
		t.Fatalf("expected body %q after reload, got %q", "after", body)
	}
}

func TestIntegrationHotReload_AddAndRemoveListeners(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	resetAndCleanup(t)
	defer resetAndCleanup(t)

	upstream := startIntegrationUpstream(t, "present")

	cfgV1 := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":19031",
				Routes: []config.Route{
					{Match: config.Match{PathPrefix: "/"}, Upstream: upstream.URL},
				},
			},
		},
	}
	applyConfigAndWait(t, cfgV1, "localhost:19031")

	resp := httpGetThroughProxy(t, "localhost:19031", "/")
	body := readBody(t, resp)
	if body != "present" {
		t.Fatalf("expected body %q on :19031, got %q", "present", body)
	}

	cfgV2 := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":19032",
				Routes: []config.Route{
					{Match: config.Match{PathPrefix: "/"}, Upstream: upstream.URL},
				},
			},
		},
	}
	applyConfigAndWait(t, cfgV2, "localhost:19032")

	resp = httpGetThroughProxy(t, "localhost:19032", "/")
	body = readBody(t, resp)
	if body != "present" {
		t.Fatalf("expected body %q on :19032, got %q", "present", body)
	}

	waitForListenerDown(t, "localhost:19031")
	conn, err := net.DialTimeout("tcp", "localhost:19031", 100*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatalf("expected :19031 to be down after removal")
	}
}

func TestIntegrationHotReload_MultipleSequentialReloads(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	resetAndCleanup(t)
	defer resetAndCleanup(t)

	upstreamA := startIntegrationUpstream(t, "A")
	upstreamB := startIntegrationUpstream(t, "B")
	upstreamC := startIntegrationUpstream(t, "C")

	upstreams := []string{upstreamA.URL, upstreamB.URL, upstreamC.URL}
	expected := []string{"A", "B", "C"}

	for i, us := range upstreams {
		cfg := &config.ReverseProxyConfig{
			Listeners: []config.Listener{
				{
					Listen: ":19033",
					Routes: []config.Route{
						{Match: config.Match{PathPrefix: "/"}, Upstream: us},
					},
				},
			},
		}
		if i == 0 {
			applyConfigAndWait(t, cfg, "localhost:19033")
		} else {
			// Same listen address, only upstream changes — hits the toUpdate path
			// which does an atomic handler swap, no listener restart needed.
			ApplyConfig(cfg)
		}

		resp := httpGetThroughProxy(t, "localhost:19033", "/")
		body := readBody(t, resp)
		if body != expected[i] {
			t.Fatalf("reload %d: expected body %q, got %q", i+1, expected[i], body)
		}
	}
}

// Error Handling

func TestIntegrationErrorHandling_UpstreamDownReturnsBadGateway(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	resetAndCleanup(t)
	defer resetAndCleanup(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "should-not-reach")
	}))
	deadURL := upstream.URL
	upstream.Close()

	cfg := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":19040",
				Routes: []config.Route{
					{Match: config.Match{PathPrefix: "/"}, Upstream: deadURL},
				},
			},
		},
	}
	applyConfigAndWait(t, cfg, "localhost:19040")

	resp := httpGetThroughProxy(t, "localhost:19040", "/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.StatusCode)
	}
}

func TestIntegrationErrorHandling_UpstreamSlowTimesOut(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	resetAndCleanup(t)
	defer resetAndCleanup(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	t.Cleanup(func() {
		upstream.CloseClientConnections()
		upstream.Close()
	})

	cfg := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":19041",
				Routes: []config.Route{
					{Match: config.Match{PathPrefix: "/"}, Upstream: upstream.URL},
				},
			},
		},
	}
	applyConfigAndWait(t, cfg, "localhost:19041")

	client := &http.Client{Timeout: 1 * time.Second}
	_, err := client.Get("http://localhost:19041/")
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
}
