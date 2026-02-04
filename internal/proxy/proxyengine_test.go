package egproxy

import (
	"context"
	"edgegate/internal/config"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type orderTrackingMiddleware struct {
	name   string
	called *[]string
}

func (m *orderTrackingMiddleware) ServerStart(ctx context.Context) {}
func (m *orderTrackingMiddleware) ServerShutdown()                 {}
func (m *orderTrackingMiddleware) WrapHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*m.called = append(*m.called, m.name)
		next.ServeHTTP(w, r)
	})
}

func TestBuildHandlerWithMiddlewares(t *testing.T) {
	called := []string{}

	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = append(called, "base")
	})

	middlewares := []HandlerMiddleware{
		&orderTrackingMiddleware{name: "mw1", called: &called},
		&orderTrackingMiddleware{name: "mw2", called: &called},
	}

	handler := buildHandlerWithMiddlewares(base, middlewares)

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Middlewares should wrap in order: mw1 -> mw2 -> base
	expected := []string{"mw1", "mw2", "base"}
	if len(called) != len(expected) {
		t.Fatalf("expected %d calls, got %d: %v", len(expected), len(called), called)
	}
	for i, name := range expected {
		if called[i] != name {
			t.Errorf("call %d: expected %q, got %q", i, name, called[i])
		}
	}
}

func TestApplyConfig_NewListener_StartsMiddlewares(t *testing.T) {
	// Reset global state
	mu.Lock()
	servers = map[string]*proxyServer{}
	mu.Unlock()

	cfg := &config.ReverseProxyConfig{
		Listeners: []config.Listener{
			{
				Listen: ":18080",
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

	// Give server time to start
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	ps, exists := servers[":18080"]
	mu.Unlock()

	if !exists {
		t.Fatal("expected server to be created")
	}

	if len(ps.middlewares) == 0 {
		t.Fatal("expected middlewares to be attached")
	}

	// Cleanup
	mu.Lock()
	delete(servers, ":18080")
	mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	ps.server.Shutdown(ctx)

	for _, mw := range ps.middlewares {
		mw.ServerShutdown()
	}
}

func TestListenerRouter_ConcurrentAccess(t *testing.T) {
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	router := newListenerRouter(base)

	var wg sync.WaitGroup

	// Concurrent reads (ServeHTTP)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				req := httptest.NewRequest("GET", "/", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
			}
		}()
	}

	// Concurrent writes (Update)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				newHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				})
				router.Update(newHandler)
			}
		}()
	}

	wg.Wait()
}
