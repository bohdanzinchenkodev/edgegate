package egproxy

import (
	"edgegate/internal/config"
	"edgegate/internal/ratelimit"
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

func (m *orderTrackingMiddleware) Equal(other any) bool { return false }
func (m *orderTrackingMiddleware) String() string       { return m.name }
func (m *orderTrackingMiddleware) WrapHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*m.called = append(*m.called, m.name)
		next.ServeHTTP(w, r)
	})
}

func TestBuildHandlerWithMiddlewares_WrapsInOrder(t *testing.T) {
	var called []string

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

func TestHandlerWrapper_ConcurrentAccess(t *testing.T) {
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	router := newHandlerWrapper(base)

	var wg sync.WaitGroup

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

type testDiffMiddleware struct {
	id string
}

func (m *testDiffMiddleware) Equal(other any) bool {
	o, ok := other.(*testDiffMiddleware)
	if !ok {
		return false
	}
	return m.id == o.id
}

func (m *testDiffMiddleware) String() string { return m.id }

func (m *testDiffMiddleware) WrapHandler(next http.Handler) http.Handler {
	return next
}

type otherDiffMiddleware struct {
	id string
}

func (m *otherDiffMiddleware) Equal(other any) bool {
	o, ok := other.(*otherDiffMiddleware)
	if !ok {
		return false
	}
	return m.id == o.id
}

func (m *otherDiffMiddleware) String() string { return m.id }

func (m *otherDiffMiddleware) WrapHandler(next http.Handler) http.Handler {
	return next
}

func TestBuildMiddlewareDiff_UnchangedMiddlewareReusesOldInstance(t *testing.T) {
	oldMW := &testDiffMiddleware{id: "same"}
	newMW := &testDiffMiddleware{id: "same"}

	diff := buildMiddlewareDiff([]HandlerMiddleware{newMW}, []HandlerMiddleware{oldMW}, ":8080")

	if len(diff.toStart) != 0 {
		t.Fatalf("expected no middlewares to start, got %d", len(diff.toStart))
	}
	if len(diff.toStop) != 0 {
		t.Fatalf("expected no middlewares to stop, got %d", len(diff.toStop))
	}
	if len(diff.current) != 1 {
		t.Fatalf("expected one middleware in current, got %d", len(diff.current))
	}
	if diff.current[0] != oldMW {
		t.Fatalf("expected current middleware to reuse old instance")
	}
}

func TestBuildMiddlewareDiff_ChangedMiddlewareReplacesInstance(t *testing.T) {
	oldMW := &testDiffMiddleware{id: "old"}
	newMW := &testDiffMiddleware{id: "new"}

	diff := buildMiddlewareDiff([]HandlerMiddleware{newMW}, []HandlerMiddleware{oldMW}, ":8080")

	if len(diff.toStart) != 1 {
		t.Fatalf("expected one middleware to start, got %d", len(diff.toStart))
	}
	if diff.toStart[0] != newMW {
		t.Fatalf("expected new middleware in toStart")
	}
	if len(diff.toStop) != 1 {
		t.Fatalf("expected one middleware to stop, got %d", len(diff.toStop))
	}
	if diff.toStop[0] != oldMW {
		t.Fatalf("expected old middleware in toStop")
	}
	if len(diff.current) != 1 {
		t.Fatalf("expected one middleware in current, got %d", len(diff.current))
	}
	if diff.current[0] != newMW {
		t.Fatalf("expected current middleware to be new instance")
	}
}

func TestBuildMiddlewareDiff_AddsAndRemovesMiddlewareByTypeAndMatch(t *testing.T) {
	oldKept := &testDiffMiddleware{id: "keep"}
	oldRemoved := &otherDiffMiddleware{id: "remove"}
	newKept := &testDiffMiddleware{id: "keep"}
	newAdded := &otherDiffMiddleware{id: "add"}

	diff := buildMiddlewareDiff(
		[]HandlerMiddleware{newKept, newAdded},
		[]HandlerMiddleware{oldKept, oldRemoved},
		":8080",
	)

	if len(diff.toStart) != 1 {
		t.Fatalf("expected one middleware to start, got %d", len(diff.toStart))
	}
	if diff.toStart[0] != newAdded {
		t.Fatalf("expected newAdded middleware in toStart")
	}
	if len(diff.toStop) != 1 {
		t.Fatalf("expected one middleware to stop, got %d", len(diff.toStop))
	}
	if diff.toStop[0] != oldRemoved {
		t.Fatalf("expected oldRemoved middleware in toStop")
	}
	if len(diff.current) != 2 {
		t.Fatalf("expected two middlewares in current, got %d", len(diff.current))
	}
	if diff.current[0] != oldKept {
		t.Fatalf("expected first current middleware to reuse old kept instance")
	}
	if diff.current[1] != newAdded {
		t.Fatalf("expected second current middleware to be newly added middleware")
	}
}

func TestCompileRouter_ValidListenerBuildsCompiledRoutes(t *testing.T) {
	l := config.Listener{
		Listen: ":8080",
		Routes: []config.Route{
			{
				Match:    config.Match{Host: "api.example.com"},
				Upstream: "http://127.0.0.1:9000",
			},
			{
				Match:    config.Match{PathPrefix: "/api"},
				Upstream: "http://127.0.0.1:9001",
			},
		},
	}

	pr, err := compileRouter(l)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(pr.routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(pr.routes))
	}
	if pr.routes[0].host != "api.example.com" {
		t.Fatalf("unexpected host in first route: %q", pr.routes[0].host)
	}
	if pr.routes[1].pathPrefix != "/api" {
		t.Fatalf("unexpected path prefix in second route: %q", pr.routes[1].pathPrefix)
	}
	if pr.routes[0].proxy == nil || pr.routes[1].proxy == nil {
		t.Fatalf("expected compiled routes to have prebuilt proxy handlers")
	}
}

func TestCompileRouter_InvalidUpstreamReturnsError(t *testing.T) {
	l := config.Listener{
		Listen: ":8080",
		Routes: []config.Route{
			{
				Match:    config.Match{PathPrefix: "/"},
				Upstream: "://bad-url",
			},
		},
	}

	_, err := compileRouter(l)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestCompileMiddlewares_RateLimitDisabledReturnsEmpty(t *testing.T) {
	mws := compileMiddlewares(config.Listener{})
	if len(mws) != 0 {
		t.Fatalf("expected no middlewares, got %d", len(mws))
	}
}

func TestCompileMiddlewares_RateLimitEnabledBuildsRateLimiter(t *testing.T) {
	l := config.Listener{}
	l.RateLimit.Enabled = true
	l.RateLimit.Requests = 100
	l.RateLimit.Window = 10 * time.Second
	l.RateLimit.ClientTTL = 30 * time.Second
	l.RateLimit.TrustedProxies = []string{"10.0.0.0/8"}

	mws := compileMiddlewares(l)
	if len(mws) != 1 {
		t.Fatalf("expected one middleware, got %d", len(mws))
	}
	if _, ok := mws[0].(*ratelimit.RateLimiter); !ok {
		t.Fatalf("expected middleware type *ratelimit.RateLimiter, got %T", mws[0])
	}
}
