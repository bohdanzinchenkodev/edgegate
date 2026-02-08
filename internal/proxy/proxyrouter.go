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

// handlerWrapper allows changing the handler of a listener without restarting the server
// Is used as the main handler for each listener and can be updated to point to a new handler when the configuration changes
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

// proxyRouter is the main handlerWrapper for the proxy server, it holds the compiled routes and matches incoming requests to the correct route and proxy handler
// It is used as the last and most important handler in the chain of handlers for each listener, and is responsible for routing requests to the correct upstream based on the host and path prefix
// by default if no middlewares are configured, the proxyRouter will be the only handler in the chain and will route requests directly to the upstreams based on the configuration
// proxyRouter Stores proxy instances for each route to avoid rebuilding them on each request
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
		// host match
		if route.host != "" && strings.EqualFold(route.host, host) {
			log.Printf("Matched host route: host=%s upstream=%s\n", route.host, route.upstream)
			route.proxy.ServeHTTP(w, r)
			return
		}
		// path prefix match
		if route.pathPrefix != "" && strings.HasPrefix(path, route.pathPrefix) {
			log.Printf("Matched path prefix route: path_prefix=%s upstream=%s\n", route.pathPrefix, route.upstream)
			route.proxy.ServeHTTP(w, r)
			return
		}
	}

	log.Printf("No matching route for host=%s path=%s\n", host, path)
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
		p := NewProxy(u) // built once per reload, reused per request

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
			UsageRate:       1, //1 request per token
			WheelSize:       int(l.RateLimit.ClientTTL.Seconds()),
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
	matched := make(map[int]bool) // tracks which old middlewares were matched

	for _, nmw := range newMiddlewares {
		found := false
		for i, omw := range oldMiddlewares {
			if matched[i] || reflect.TypeOf(nmw) != reflect.TypeOf(omw) {
				continue
			}

			if omw.Equal(nmw) {
				//if no changes detected then use old instance
				diff.current = append(diff.current, omw)
			} else {
				//config changed — stop old, start new
				log.Printf("[config] listener %s: middleware %T config changed\nold: %s\nnew: %s", listenAddr, nmw, omw, nmw)
				diff.toStop = append(diff.toStop, omw)
				diff.toStart = append(diff.toStart, nmw)
				diff.current = append(diff.current, nmw)
			}
			matched[i] = true
			found = true
			break
		}
		//we didn't find a match in old slice so we just add new mw
		if !found {
			log.Printf("[config] listener %s: middleware %T added", listenAddr, nmw)
			diff.toStart = append(diff.toStart, nmw)
			diff.current = append(diff.current, nmw)
		}
	}

	// old middlewares that weren't matched need to be stopped
	for i, omw := range oldMiddlewares {
		if !matched[i] {
			log.Printf("[config] listener %s: middleware %T removed", listenAddr, omw)
			diff.toStop = append(diff.toStop, omw)
		}
	}

	return diff
}
