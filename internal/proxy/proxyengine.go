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
)

type proxyServer struct {
	server      *http.Server
	done        chan struct{}
	router      *listenerRouter
	middlewares []HandlerMiddleware
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
	listen      string
	routes      []compiledRoute
	middlewares []HandlerMiddleware
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

func compileListener(l config.Listener) (*compiledListener, error) {
	cl := &compiledListener{listen: l.Listen}
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
	// Rate limiter middleware
	//If it isn't present, no rate limiting is applied
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
		//create rate limiter
		rl := ratelimit.NewRateLimiter(o)
		cl.middlewares = append(cl.middlewares, rl)
	}

	return cl, nil
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
	// 1) compile new listeners (outside lock - no shared state access)
	newCompiled := make(map[string]*compiledListener, len(cfg.Listeners))
	for _, l := range cfg.Listeners {
		cl, err := compileListener(l)
		if err != nil {
			log.Print(err)
			continue
		}
		newCompiled[cl.listen] = cl
	}

	mu.Lock()
	defer mu.Unlock()

	// 2) decide what to start/update/stop
	var (
		toStart  []*proxyServer
		toStop   []*proxyServer
		toUpdate []struct {
			ps             *proxyServer
			handler        http.Handler
			newMiddlewares []HandlerMiddleware
		}
	)

	// update existing or create new
	for addr, cl := range newCompiled {
		if ps, exists := servers[addr]; exists {
			toUpdate = append(toUpdate, struct {
				ps             *proxyServer
				handler        http.Handler
				newMiddlewares []HandlerMiddleware
			}{
				ps:             ps,
				handler:        cl,
				newMiddlewares: cl.middlewares,
			})
		} else {
			router := newListenerRouter(buildHandlerWithMiddlewares(cl, cl.middlewares))

			srv := &http.Server{
				Addr:    addr,
				Handler: router,
			}

			ps := &proxyServer{
				server:      srv,
				done:        make(chan struct{}),
				router:      router,
				middlewares: cl.middlewares,
			}

			servers[addr] = ps
			toStart = append(toStart, ps)
		}
	}

	// stop removed listeners
	for addr, ps := range servers {
		if _, stillExists := newCompiled[addr]; !stillExists {
			delete(servers, addr)
			toStop = append(toStop, ps)
		}
	}

	// 3) perform updates, starts, stops (all under lock to protect ps.middlewares)
	for _, upd := range toUpdate {
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

	for _, ps := range toStart {
		// start middlewares BEFORE server accepts requests
		for _, mw := range ps.middlewares {
			mw.ServerStart(context.Background())
		}
		go startServer(ps)
	}

	for _, ps := range toStop {
		for _, mw := range ps.middlewares {
			mw.ServerShutdown()
		}
		go shutdownServer(ps)
	}
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
