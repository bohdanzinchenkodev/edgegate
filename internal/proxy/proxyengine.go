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

var (
	servers []*proxyServer
	mu      sync.Mutex
)

type proxyServer struct {
	server *http.Server
	done   chan struct{}
}
type compiledListener struct {
	listen string
	routes []compiledRoute
}

type compiledRoute struct {
	host       string
	pathPrefix string
	upstream   string
	proxy      http.Handler
}

func StartEngine(ctx context.Context, configPath string) {
	go func() {
		<-ctx.Done()
		shutdownActiveServers()
	}()

	fw := config.NewFileWatcher(configPath)
	fw.ReturnBytesOnInit = true

	fw.FileChangedHandler = func(file []byte) {
		shutdownActiveServers()

		cfg, err := config.ParseConfig(file)
		if err != nil {
			log.Print(err)
			return
		}
		initEngine(cfg)
	}

	fw.ErrorHandler = func(err error) {
		log.Print(err)
	}

	fw.Watch(ctx)
}

func initEngine(cfg *config.ReverseProxyConfig) {
	newServers := make([]*proxyServer, 0)

	for _, l := range cfg.Listeners {
		serv := &http.Server{Addr: l.Listen}

		mux := http.NewServeMux()
		cl, err := compileListener(l)
		if err != nil {
			log.Print(err)
			continue
		}

		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler, err := getHandler(r, cl)
			if err != nil {
				http.Error(w, "Bad Gateway", http.StatusBadGateway)
				return
			}

			handler.ServeHTTP(w, r)
		}))
		serv.Handler = mux

		ps := &proxyServer{
			server: serv,
			done:   make(chan struct{}),
		}
		newServers = append(newServers, ps)
		go startServer(ps)
	}

	mu.Lock()
	servers = newServers
	mu.Unlock()
}
func compileListener(l config.Listener) (*compiledListener, error) {
	cl := &compiledListener{
		listen: l.Listen,
	}
	for _, r := range l.Routes {
		upstreamUrl, err := url.Parse(r.Upstream)
		if err != nil {
			return nil, err
		}
		proxy := NewProxy(upstreamUrl)
		cl.routes = append(cl.routes, compiledRoute{
			host:       r.Match.Host,
			pathPrefix: r.Match.PathPrefix,
			upstream:   r.Upstream,
			proxy:      proxy,
		})
	}
	return cl, nil
}
func getHandler(r *http.Request, cl *compiledListener) (http.Handler, error) {
	host := stripPort(r.Host)
	path := r.URL.Path
	for _, route := range cl.routes {
		//check host match
		log.Printf("Checking route: host=%v,path_prefix=%v -> upstream=%v\n", route.host, route.pathPrefix, route.upstream)
		if route.host != "" && strings.EqualFold(route.host, host) {
			return route.proxy, nil
		}
		//check path prefix match
		if route.pathPrefix != "" && strings.HasPrefix(path, route.pathPrefix) {
			return route.proxy, nil
		}
	}
	return nil, errors.New("no matching routes found")

}

func startServer(ps *proxyServer) {
	log.Printf("Starting server on address: %v\n", ps.server.Addr)
	err := ps.server.ListenAndServe()
	close(ps.done)

	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Print(err)
	}

	log.Printf("server on %v stopped\n", ps.server.Addr)
}

func shutdownActiveServers() {
	mu.Lock()
	old := servers
	servers = nil
	mu.Unlock()

	if len(old) == 0 {
		return
	}

	var wg sync.WaitGroup
	wg.Add(len(old))

	for _, s := range old {
		s := s
		go func() {
			defer wg.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = s.server.Shutdown(shutdownCtx)
			<-s.done
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
