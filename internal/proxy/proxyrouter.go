package egproxy

import (
	"edgegate/internal/config"
	"edgegate/internal/ratelimit"
	"log"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"time"
)

// handlerWrapper swaps listener handlers atomically during config reloads.
type handlerWrapper struct {
	current atomic.Value // stores http.Handler
}

func newHandlerWrapper(initial http.Handler) *handlerWrapper {
	hw := &handlerWrapper{}
	hw.current.Store(initial)
	return hw
}

func (hw *handlerWrapper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cl := hw.current.Load().(http.Handler)
	cl.ServeHTTP(w, r)
}

func (hw *handlerWrapper) Update(h http.Handler) {
	hw.current.Store(h)
}

// proxyRouter holds precompiled routes and forwards to matching upstreams.
type proxyRouter struct {
	routes []compiledRoute
}
type compiledRoute struct {
	host       string
	pathPrefix string
	upstream   string
	proxy      http.Handler // prebuilt proxy
}

func (pr *proxyRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := stripPort(r.Host)
	path := r.URL.Path

	for _, route := range pr.routes {
		if route.host != "" && !strings.EqualFold(route.host, host) {
			continue
		}
		if route.pathPrefix != "" && !strings.HasPrefix(path, route.pathPrefix) {
			continue
		}
		route.proxy.ServeHTTP(w, r)
		return
	}
	http.Error(w, "Bad Gateway", http.StatusBadGateway)
}
func (pr *proxyRouter) Equal(other *proxyRouter) bool {
	if len(pr.routes) != len(other.routes) {
		return false
	}
	for i, r := range pr.routes {
		or := other.routes[i]
		if r.host != or.host || r.pathPrefix != or.pathPrefix || r.upstream != or.upstream {
			return false
		}
	}
	return true
}
func compileRouter(l config.Listener) (*proxyRouter, error) {
	pr := &proxyRouter{}
	pr.routes = make([]compiledRoute, 0, len(l.Routes))

	for _, r := range l.Routes {
		u, err := url.Parse(r.Upstream)
		if err != nil {
			return nil, err
		}
		p := NewProxy(u)
		if r.Match.PathPrefix != "" {
			p.Prefix = r.Match.PathPrefix
		}

		pr.routes = append(pr.routes, compiledRoute{
			host:       r.Match.Host,
			pathPrefix: r.Match.PathPrefix,
			upstream:   r.Upstream,
			proxy:      p,
		})
	}

	return pr, nil
}

func compileMiddlewares(l config.Listener) []HandlerMiddleware {
	mws := make([]HandlerMiddleware, 0)
	if l.RateLimit.Enabled {
		refillRate := float64(l.RateLimit.Requests) / l.RateLimit.Window.Seconds()
		o := ratelimit.RateLimiterOption{
			Capacity:        l.RateLimit.Requests,
			RefillRate:      refillRate,
			TrustedProxies:  l.RateLimit.TrustedProxies,
			UsageRate:       1,               //1 request per token
			CleanupInterval: 1 * time.Second, //cleanup every second
			DeleteAfter:     l.RateLimit.ClientTTL,
		}
		rl := ratelimit.NewRateLimiter(o)
		mws = append(mws, rl)
	}
	return mws
}
func buildHandlerWithMiddlewares(base http.Handler, mws []HandlerMiddleware) http.Handler {
	handler := base
	for i := len(mws) - 1; i >= 0; i-- {
		handler = mws[i].WrapHandler(handler)
	}
	return http.HandlerFunc(handler.ServeHTTP)
}
func buildMiddlewareDiff(newMiddlewares, oldMiddlewares []HandlerMiddleware, listenAddr string) middlewareDiff {
	diff := middlewareDiff{}
	matched := make(map[int]bool)

	for _, nmw := range newMiddlewares {
		found := false
		for i, omw := range oldMiddlewares {
			if matched[i] || reflect.TypeOf(nmw) != reflect.TypeOf(omw) {
				continue
			}

			if omw.Equal(nmw) {
				// Keep old instance when config is unchanged.
				diff.current = append(diff.current, omw)
			} else {
				// Config changed: stop old and start new.
				log.Printf("[config] listener %s: middleware %T config changed\nold: %s\nnew: %s", listenAddr, nmw, omw, nmw)
				diff.toStop = append(diff.toStop, omw)
				diff.toStart = append(diff.toStart, nmw)
				diff.current = append(diff.current, nmw)
			}
			matched[i] = true
			found = true
			break
		}
		// No matching old middleware; add as new.
		if !found {
			log.Printf("[config] listener %s: middleware %T added", listenAddr, nmw)
			diff.toStart = append(diff.toStart, nmw)
			diff.current = append(diff.current, nmw)
		}
	}

	// Old middlewares that were not matched must be stopped.
	for i, omw := range oldMiddlewares {
		if !matched[i] {
			log.Printf("[config] listener %s: middleware %T removed", listenAddr, omw)
			diff.toStop = append(diff.toStop, omw)
		}
	}

	return diff
}
