package egproxy

import (
	"context"
	"edgegate/internal/config"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Global state for running proxy servers
// server -> listenerRouter (ServeHTTP) -> compiledListener (ServeHTTP) -> compiledRoute -> proxy (ServeHTTP)
var (
	mu      sync.Mutex
	servers = map[string]*proxyServer{}
)

type proxyServer struct {
	server *http.Server
	done   chan struct{}
	router *listenerRouter
}

type listenerRouter struct {
	mu sync.RWMutex
	cl *compiledListener
}

func newListenerRouter(initial *compiledListener) *listenerRouter {
	return &listenerRouter{cl: initial}
}

func (lr *listenerRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	lr.mu.RLock()
	cl := lr.cl
	lr.mu.RUnlock()

	// delegate to compiled routes
	cl.ServeHTTP(w, r)
}

func (lr *listenerRouter) Update(newCL *compiledListener) {
	lr.mu.Lock()
	lr.cl = newCL
	lr.mu.Unlock()
}

type compiledListener struct {
	listen string
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
			route.proxy.ServeHTTP(w, r)
			return
		}
		// path prefix match
		if route.pathPrefix != "" && strings.HasPrefix(path, route.pathPrefix) {
			route.proxy.ServeHTTP(w, r)
			return
		}
	}

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
	return cl, nil
}

func StartEngine(ctx context.Context, configPath string) {
	go func() {
		<-ctx.Done()
		shutdownAll()
	}()

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
}

func applyConfig(cfg *config.ReverseProxyConfig) {
	// 1) compile new listeners
	newCompiled := make(map[string]*compiledListener, len(cfg.Listeners))
	for _, l := range cfg.Listeners {
		cl, err := compileListener(l)
		if err != nil {
			log.Print(err)
			continue
		}
		newCompiled[cl.listen] = cl
	}

	// 2) decide what to start/update/stop
	var (
		toStart  []*proxyServer
		toStop   []*proxyServer
		toUpdate []struct {
			ps *proxyServer
			cl *compiledListener
		}
	)

	mu.Lock()

	// update existing or create new
	for addr, cl := range newCompiled {
		if ps, exists := servers[addr]; exists {
			toUpdate = append(toUpdate, struct {
				ps *proxyServer
				cl *compiledListener
			}{ps: ps, cl: cl})
		} else {
			router := newListenerRouter(cl)

			srv := &http.Server{
				Addr:    addr,
				Handler: router,
			}

			ps := &proxyServer{
				server: srv,
				done:   make(chan struct{}),
				router: router,
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

	mu.Unlock()

	// 3) perform actions outside global lock
	for _, upd := range toUpdate {
		upd.ps.router.Update(upd.cl)
	}

	for _, ps := range toStart {
		go startServer(ps)
	}

	for _, ps := range toStop {
		go shutdownServer(ps)
	}
}

func startServer(ps *proxyServer) {
	log.Printf("Starting server on %v\n", ps.server.Addr)

	err := ps.server.ListenAndServe()
	close(ps.done)

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
	mu.Unlock()

	for _, ps := range old {
		go shutdownServer(ps)
	}
}

func stripPort(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}
