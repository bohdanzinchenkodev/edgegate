package egproxy

import (
	"edgegate/internal/config"
	"testing"
	"time"
)

func TestApplyConfig_NewListenerStartsWithoutMiddlewares(t *testing.T) {
	useNoopServerLifecycleHooks(t)
	resetEngineStateForTest()
	defer resetEngineStateForTest()

	cfg := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":0",
				Routes: []config.Route{
					{
						Match:    config.Match{PathPrefix: "/"},
						Upstream: "http://localhost:9999",
					},
				},
			},
		},
	}

	applyConfig(cfg)

	mu.Lock()
	ps, exists := servers[":0"]
	mu.Unlock()

	if !exists {
		t.Fatal("expected server to be created")
	}

	if len(ps.middlewares) > 0 {
		t.Fatalf("expected no middlewares, got %d", len(ps.middlewares))
	}

	if len(currCfg.Listeners) != 1 {
		t.Fatalf("expected currCfg to have one listener, got %d", len(currCfg.Listeners))
	}
	if currCfg.Listeners[0].Listen != ":0" {
		t.Fatalf("expected currCfg listener :0, got %s", currCfg.Listeners[0].Listen)
	}

}

func TestApplyConfig_ReapplySameConfigKeepsServerInstance(t *testing.T) {
	useNoopServerLifecycleHooks(t)
	resetEngineStateForTest()
	defer resetEngineStateForTest()

	cfg := &config.ReverseProxyConfig{Listeners: []config.Listener{newListenerForTest(":0", "http://127.0.0.1:9010")}}

	applyConfig(cfg)

	mu.Lock()
	first := servers[":0"]
	mu.Unlock()
	if first == nil {
		t.Fatalf("expected server after first apply")
	}

	applyConfig(cfg)

	mu.Lock()
	second := servers[":0"]
	mu.Unlock()
	if second == nil {
		t.Fatalf("expected server after second apply")
	}
	if first != second {
		t.Fatalf("expected server instance to be reused on identical config")
	}
}

func TestApplyConfig_TLSToggleReplacesServerInstance(t *testing.T) {
	useNoopServerLifecycleHooks(t)
	resetEngineStateForTest()
	defer resetEngineStateForTest()

	oldCfg := &config.ReverseProxyConfig{Listeners: []config.Listener{newListenerForTest(":0", "http://127.0.0.1:9011")}}
	applyConfig(oldCfg)

	mu.Lock()
	first := servers[":0"]
	mu.Unlock()
	if first == nil {
		t.Fatalf("expected initial server")
	}

	newListener := newListenerForTest(":0", "http://127.0.0.1:9011")
	newListener.Tls.Enabled = true
	newListener.Tls.DefaultCertFile = "./missing-cert.pem"
	newListener.Tls.DefaultKeyFile = "./missing-key.pem"
	newCfg := &config.ReverseProxyConfig{Listeners: []config.Listener{newListener}}

	applyConfig(newCfg)

	mu.Lock()
	second := servers[":0"]
	mu.Unlock()
	if second == nil {
		t.Fatalf("expected replaced server")
	}
	if first == second {
		t.Fatalf("expected server instance replacement after TLS toggle")
	}
	if second.tlsManager == nil {
		t.Fatalf("expected TLS manager on replaced server")
	}
}

func TestApplyConfig_RouteChangeUpdatesRouterInPlace(t *testing.T) {
	useNoopServerLifecycleHooks(t)
	resetEngineStateForTest()
	defer resetEngineStateForTest()

	oldCfg := &config.ReverseProxyConfig{Listeners: []config.Listener{newListenerForTest(":0", "http://127.0.0.1:9012")}}
	applyConfig(oldCfg)

	mu.Lock()
	first := servers[":0"]
	oldRouter := first.router
	mu.Unlock()
	if first == nil {
		t.Fatalf("expected initial server")
	}

	newCfg := &config.ReverseProxyConfig{Listeners: []config.Listener{newListenerForTest(":0", "http://127.0.0.1:9013")}}
	applyConfig(newCfg)

	mu.Lock()
	second := servers[":0"]
	mu.Unlock()
	if second == nil {
		t.Fatalf("expected server after update")
	}
	if first != second {
		t.Fatalf("expected in-place update, not server replacement")
	}
	if second.router == oldRouter {
		t.Fatalf("expected router instance to change on route update")
	}
	if second.tlsManager != nil {
		t.Fatalf("expected TLS manager to remain nil")
	}
}

func TestCompareConfigs_NewListenerPlansStart(t *testing.T) {
	resetEngineStateForTest()

	oldCfg := &config.ReverseProxyConfig{}
	newCfg := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			newListenerForTest(":18080", "http://127.0.0.1:9000"),
		},
	}

	res := compareConfigs(oldCfg, newCfg)

	if len(res.toStart) != 1 {
		t.Fatalf("expected one server to start, got %d", len(res.toStart))
	}
	if len(res.toUpdate) != 0 || len(res.toReplace) != 0 || len(res.toStop) != 0 {
		t.Fatalf("expected only start actions, got update=%d replace=%d stop=%d", len(res.toUpdate), len(res.toReplace), len(res.toStop))
	}
}

func TestCompareConfigs_RemovedListenerPlansStop(t *testing.T) {
	resetEngineStateForTest()

	listener := newListenerForTest(":18081", "http://127.0.0.1:9001")
	servers[listener.Listen] = mustProxyServerForListener(t, listener)

	oldCfg := &config.ReverseProxyConfig{Listeners: []config.Listener{listener}}
	newCfg := &config.ReverseProxyConfig{}

	res := compareConfigs(oldCfg, newCfg)

	if len(res.toStop) != 1 {
		t.Fatalf("expected one server to stop, got %d", len(res.toStop))
	}
	if _, exists := servers[listener.Listen]; exists {
		t.Fatalf("expected removed listener to be deleted from servers map")
	}
}

func TestCompareConfigs_UnchangedListenerNoOps(t *testing.T) {
	resetEngineStateForTest()

	listener := newListenerForTest(":18082", "http://127.0.0.1:9002")
	servers[listener.Listen] = mustProxyServerForListener(t, listener)

	oldCfg := &config.ReverseProxyConfig{Listeners: []config.Listener{listener}}
	newCfg := &config.ReverseProxyConfig{Listeners: []config.Listener{listener}}

	res := compareConfigs(oldCfg, newCfg)

	if len(res.toStart) != 0 || len(res.toUpdate) != 0 || len(res.toReplace) != 0 || len(res.toStop) != 0 {
		t.Fatalf("expected no actions, got start=%d update=%d replace=%d stop=%d", len(res.toStart), len(res.toUpdate), len(res.toReplace), len(res.toStop))
	}
}

func TestCompareConfigs_RouteChangePlansUpdate(t *testing.T) {
	resetEngineStateForTest()

	oldListener := newListenerForTest(":18083", "http://127.0.0.1:9003")
	newListener := newListenerForTest(":18083", "http://127.0.0.1:9004")
	servers[oldListener.Listen] = mustProxyServerForListener(t, oldListener)

	oldCfg := &config.ReverseProxyConfig{Listeners: []config.Listener{oldListener}}
	newCfg := &config.ReverseProxyConfig{Listeners: []config.Listener{newListener}}

	res := compareConfigs(oldCfg, newCfg)

	if len(res.toUpdate) != 1 {
		t.Fatalf("expected one server update, got %d", len(res.toUpdate))
	}
	if len(res.toStart) != 0 || len(res.toReplace) != 0 || len(res.toStop) != 0 {
		t.Fatalf("expected only update actions")
	}
}

func TestCompareConfigs_TLSTogglePlansReplace(t *testing.T) {
	resetEngineStateForTest()

	oldListener := newListenerForTest(":18084", "http://127.0.0.1:9005")
	newListener := newListenerForTest(":18084", "http://127.0.0.1:9005")
	newListener.Tls.Enabled = true
	newListener.Tls.DefaultCertFile = "./new-cert.pem"
	newListener.Tls.DefaultKeyFile = "./new-key.pem"
	servers[oldListener.Listen] = mustProxyServerForListener(t, oldListener)

	oldCfg := &config.ReverseProxyConfig{Listeners: []config.Listener{oldListener}}
	newCfg := &config.ReverseProxyConfig{Listeners: []config.Listener{newListener}}

	res := compareConfigs(oldCfg, newCfg)

	if len(res.toReplace) != 1 {
		t.Fatalf("expected one server replacement, got %d", len(res.toReplace))
	}
	if len(res.toUpdate) != 0 {
		t.Fatalf("expected no in-place updates when TLS is toggled")
	}
}

func TestCompareConfigs_TLSCertChangePlansUpdate(t *testing.T) {
	resetEngineStateForTest()

	oldListener := newListenerForTest(":18085", "http://127.0.0.1:9006")
	oldListener.Tls.Enabled = true
	oldListener.Tls.DefaultCertFile = "./old-cert.pem"
	oldListener.Tls.DefaultKeyFile = "./old-key.pem"

	newListener := oldListener
	newListener.Tls.DefaultCertFile = "./new-cert.pem"
	newListener.Tls.DefaultKeyFile = "./new-key.pem"

	servers[oldListener.Listen] = mustProxyServerForListener(t, oldListener)

	oldCfg := &config.ReverseProxyConfig{Listeners: []config.Listener{oldListener}}
	newCfg := &config.ReverseProxyConfig{Listeners: []config.Listener{newListener}}

	res := compareConfigs(oldCfg, newCfg)

	if len(res.toUpdate) != 1 {
		t.Fatalf("expected one server update, got %d", len(res.toUpdate))
	}
	if res.toUpdate[0].newTlsManager == nil {
		t.Fatalf("expected TLS manager reload data in update")
	}
}

func TestCompareConfigs_MiddlewareChangePlansUpdate(t *testing.T) {
	resetEngineStateForTest()

	oldListener := newListenerForTest(":18086", "http://127.0.0.1:9007")
	oldListener.RateLimit.Enabled = true
	oldListener.RateLimit.Requests = 10
	oldListener.RateLimit.Window = 1 * time.Second
	oldListener.RateLimit.ClientTTL = 10 * time.Second

	newListener := oldListener
	newListener.RateLimit.Requests = 20

	servers[oldListener.Listen] = mustProxyServerForListener(t, oldListener)

	oldCfg := &config.ReverseProxyConfig{Listeners: []config.Listener{oldListener}}
	newCfg := &config.ReverseProxyConfig{Listeners: []config.Listener{newListener}}

	res := compareConfigs(oldCfg, newCfg)

	if len(res.toUpdate) != 1 {
		t.Fatalf("expected one server update, got %d", len(res.toUpdate))
	}
	if len(res.toUpdate[0].mwDiff.toStart) != 1 || len(res.toUpdate[0].mwDiff.toStop) != 1 {
		t.Fatalf("expected middleware replacement, got toStart=%d toStop=%d", len(res.toUpdate[0].mwDiff.toStart), len(res.toUpdate[0].mwDiff.toStop))
	}
}

func TestCompareConfigs_InvalidRouteSkipsListener(t *testing.T) {
	resetEngineStateForTest()

	oldCfg := &config.ReverseProxyConfig{}
	newCfg := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":18087",
				Routes: []config.Route{
					{Match: config.Match{PathPrefix: "/"}, Upstream: "://bad-url"},
				},
			},
		},
	}

	res := compareConfigs(oldCfg, newCfg)

	if len(res.toStart) != 0 {
		t.Fatalf("expected no server starts for invalid route compile, got %d", len(res.toStart))
	}
	if _, exists := servers[":18087"]; exists {
		t.Fatalf("expected invalid listener to be skipped")
	}
}

func resetEngineStateForTest() {
	mu.Lock()
	servers = map[string]*proxyServer{}
	currCfg = config.ReverseProxyConfig{}
	mu.Unlock()
}

func newListenerForTest(listen, upstream string) config.Listener {
	return config.Listener{
		Listen: listen,
		Routes: []config.Route{
			{Match: config.Match{PathPrefix: "/"}, Upstream: upstream},
		},
	}
}

func mustProxyServerForListener(t *testing.T, l config.Listener) *proxyServer {
	t.Helper()
	pr, err := compileRouter(l)
	if err != nil {
		t.Fatalf("compileRouter: %v", err)
	}
	mws := compileMiddlewares(l)
	tm := compileTlsManager(l)
	return newProxyServer(l, pr, mws, tm)
}

func useNoopServerLifecycleHooks(t *testing.T) {
	t.Helper()

	prevStartAsync := startServerAsyncFn
	prevShutdownAsync := shutdownServerAsyncFn
	prevShutdownSync := shutdownServerSyncFn

	startServerAsyncFn = func(ps *proxyServer) {}
	shutdownServerAsyncFn = func(ps *proxyServer) {}
	shutdownServerSyncFn = func(ps *proxyServer) {}

	t.Cleanup(func() {
		startServerAsyncFn = prevStartAsync
		shutdownServerAsyncFn = prevShutdownAsync
		shutdownServerSyncFn = prevShutdownSync
	})
}
