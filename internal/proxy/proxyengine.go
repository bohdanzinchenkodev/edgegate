package egproxy

import (
	"context"
	"edgegate/internal/config"
	"edgegate/internal/ratelimit"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Global state for running proxy servers
// server -> listenerRouter (ServeHTTP) -> compiledListener (ServeHTTP) -> compiledRoute -> proxy (ServeHTTP)
var (
	mu      sync.Mutex
	servers = map[string]*proxyServer{}
	currCfg = config.ReverseProxyConfig{}
)

type proxyServer struct {
	server      *http.Server
	done        chan struct{}
	router      *listenerRouter
	middlewares []HandlerMiddleware
}
type compareConfigsResult struct {
	toStop   []*proxyServer
	toStart  []*proxyServer
	toUpdate []*toUpdateServer
}
type toUpdateServer struct {
	ps             *proxyServer
	handler        http.Handler
	newMiddlewares []HandlerMiddleware
}

type listenerRouter struct {
	current atomic.Value // stores http.Handler
}

func newListenerRouter(initial http.Handler) *listenerRouter {
	lr := &listenerRouter{}
	lr.current.Store(initial)
	return lr
}

func (lr *listenerRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cl := lr.current.Load().(http.Handler)
	cl.ServeHTTP(w, r)
}

func (lr *listenerRouter) Update(h http.Handler) {
	lr.current.Store(h)
}

type compiledListener struct {
	routes []compiledRoute
}

type compiledRoute struct {
	host       string
	pathPrefix string
	upstream   string
	proxy      http.Handler // prebuilt proxy
}

func (cl *compiledListener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := stripPort(r.Host)
	path := r.URL.Path

	for _, route := range cl.routes {
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
func compileListener(l config.Listener) (*compiledListener, error) {
	cl := &compiledListener{}
	cl.routes = make([]compiledRoute, 0, len(l.Routes))

	for _, r := range l.Routes {
		u, err := url.Parse(r.Upstream)
		if err != nil {
			return nil, err
		}
		p := NewProxy(u) // built once per reload, reused per request

		cl.routes = append(cl.routes, compiledRoute{
			host:       r.Match.Host,
			pathPrefix: r.Match.PathPrefix,
			upstream:   r.Upstream,
			proxy:      p,
		})
	}

	return cl, nil
}

func compareConfigs(oldCfg, newCfg *config.ReverseProxyConfig) compareConfigsResult {
	result := compareConfigsResult{}

	// Build map from old config, keyed by listen address
	oldMap := make(map[string]config.Listener, len(oldCfg.Listeners))
	for _, l := range oldCfg.Listeners {
		oldMap[l.Listen] = l
	}

	// Iterate new config: find added + updated
	for _, l := range newCfg.Listeners {
		cl, err := compileListener(l)
		if err != nil {
			log.Print(err)
			continue
		}
		middlewares := compileMiddlewares(l)
		ps, exists := servers[l.Listen]
		if !exists {
			// New listener — create and mark for start
			log.Printf("[config] listener %s: added", l.Listen)
			router := newListenerRouter(buildHandlerWithMiddlewares(cl, middlewares))
			ps = &proxyServer{
				server:      &http.Server{Addr: l.Listen, Handler: router},
				done:        make(chan struct{}),
				router:      router,
				middlewares: middlewares,
			}
			servers[l.Listen] = ps
			result.toStart = append(result.toStart, ps)
		} else {
			//check if routes changed
			oldListener, ok := oldMap[l.Listen]
			if !ok {
				panic("inconsistent state: listener not found in oldMap")
			}
			// Remove from oldMap to mark as processed
			delete(oldMap, l.Listen)

			var handler http.Handler
			routesChanged := !reflect.DeepEqual(oldListener.Routes, l.Routes)
			if routesChanged {
				log.Printf("[config] listener %s: routes changed", l.Listen)
				handler = cl
			} else {
				//if routes didn't change then we reuse old handler (compiledListener)
				handler = ps.router.current.Load().(http.Handler)
			}
			// we want to reuse old middlewares if they are the same (e.g. rate limiter with same config) to avoid unnecessary resets and state loss
			newMiddlewares, middlewaresChanged := buildNewMiddlewares(middlewares, ps.middlewares, l.Listen)
			if !routesChanged && !middlewaresChanged {
				continue // no changes, skip
			}
			log.Printf("[config] listener %s: updating (routes=%v middlewares=%v)", l.Listen, routesChanged, middlewaresChanged)
			result.toUpdate = append(result.toUpdate, &toUpdateServer{
				ps:             ps,
				handler:        handler,
				newMiddlewares: newMiddlewares,
			})
		}
	}

	// Remaining in oldMap were removed
	for addr := range oldMap {
		if ps, exists := servers[addr]; exists {
			log.Printf("[config] listener %s: removed", addr)
			delete(servers, addr)
			result.toStop = append(result.toStop, ps)
		}
	}

	return result
}
func StartEngine(ctx context.Context, configPath string) {

	fw := config.NewFileWatcher(configPath)
	fw.ReturnBytesOnInit = true

	fw.FileChangedHandler = func(file []byte) {
		cfg, err := config.ParseConfig(file)
		if err != nil {
			log.Print(err)
			return
		}
		applyConfig(cfg)
	}

	fw.ErrorHandler = func(err error) { log.Print(err) }

	fw.Watch(ctx)
	//watch returns when ctx.Done()
	//it means we should shut down all servers
	shutdownAll()
}

func applyConfig(cfg *config.ReverseProxyConfig) {

	mu.Lock()
	defer mu.Unlock()

	comparisonRes := compareConfigs(&currCfg, cfg)

	for _, upd := range comparisonRes.toUpdate {
		// start new middlewares BEFORE making handler live
		for _, mw := range upd.newMiddlewares {
			mw.ServerStart(context.Background())
		}
		handler := buildHandlerWithMiddlewares(upd.handler, upd.newMiddlewares)
		upd.ps.router.Update(handler)
		// shutdown old middlewares AFTER handler is swapped
		for _, mw := range upd.ps.middlewares {
			mw.ServerShutdown()
		}
		upd.ps.middlewares = upd.newMiddlewares
	}

	for _, ps := range comparisonRes.toStart {
		// start middlewares BEFORE server accepts requests
		for _, mw := range ps.middlewares {
			mw.ServerStart(context.Background())
		}
		go startServer(ps)
	}

	for _, ps := range comparisonRes.toStop {
		for _, mw := range ps.middlewares {
			mw.ServerShutdown()
		}
		go shutdownServer(ps)
	}

	currCfg = *cfg
}

func startServer(ps *proxyServer) {
	log.Printf("Starting server on %v\n", ps.server.Addr)

	err := ps.server.ListenAndServe()
	defer close(ps.done)

	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Print(err)
	}
	log.Printf("Server on %v stopped\n", ps.server.Addr)
}

func shutdownServer(ps *proxyServer) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = ps.server.Shutdown(shutdownCtx)
	<-ps.done
}

func shutdownAll() {
	mu.Lock()
	old := servers
	servers = map[string]*proxyServer{}
	// shutdown middlewares while holding lock (protects ps.middlewares access)
	for _, ps := range old {
		for _, mw := range ps.middlewares {
			mw.ServerShutdown()
		}
	}
	mu.Unlock()

	// shutdown HTTP servers in parallel (no lock needed - doesn't access middlewares)
	var wg sync.WaitGroup
	wg.Add(len(old))
	for _, ps := range old {
		ps := ps
		go func() {
			shutdownServer(ps)
			wg.Done()
		}()
	}
	wg.Wait()
}

func stripPort(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}
func buildHandlerWithMiddlewares(base http.Handler, mws []HandlerMiddleware) http.Handler {
	handler := base
	for i := len(mws) - 1; i >= 0; i-- {
		handler = mws[i].WrapHandler(handler)
	}
	return http.HandlerFunc(handler.ServeHTTP)
}
func buildNewMiddlewares(newMiddlewares, oldMiddlewares []HandlerMiddleware, listenAddr string) (res []HandlerMiddleware, changeDetected bool) {
	for _, nmw := range newMiddlewares {
		found := false
		for _, omw := range oldMiddlewares {
			if reflect.TypeOf(nmw) != reflect.TypeOf(omw) {
				continue
			}

			if omw.Equal(nmw) {
				//if no changes detected then use old instance
				res = append(res, omw)
			} else {
				//otherwise use new instance
				log.Printf("[config] listener %s: middleware %T config changed\nold: %s\nnew: %s", listenAddr, nmw, omw, nmw)
				res = append(res, nmw)
				changeDetected = true
			}
			found = true
			break
		}
		//we didn't find a match in old slice so we just add new mw
		if !found {
			log.Printf("[config] listener %s: middleware %T added", listenAddr, nmw)
			res = append(res, nmw)
			changeDetected = true
		}
	}
	return res, changeDetected
}
