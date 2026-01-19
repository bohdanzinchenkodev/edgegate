package egproxy

import (
	"context"
	config2 "edgegate/internal/config"
	"errors"
	"log"
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

func StartEngine(ctx context.Context, configPath string) {
	go func() {
		<-ctx.Done()
		shutdownActiveServers()
	}()

	fw := config2.NewFileWatcher(configPath)
	fw.ReturnBytesOnInit = true

	fw.FileChangedHandler = func(file []byte) {
		shutdownActiveServers()

		cfg, err := config2.ParseConfig(file)
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

func initEngine(cfg *config2.ReverseProxyConfig) {
	newServers := make([]*proxyServer, 0)

	for _, l := range cfg.Listeners {
		serv := &http.Server{Addr: l.Listen}

		mux := http.NewServeMux()

		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upstream, err := getUpstream(r, &l)
			if err != nil {
				http.Error(w, "Bad Gateway", http.StatusBadGateway)
				return
			}
			proxy := NewProxy(upstream)
			proxy.ServeHTTP(w, r)
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
func getUpstream(r *http.Request, listener *config2.Listener) (*url.URL, error) {
	host := r.Host
	path := r.URL.Path
	for _, route := range listener.Routes {
		//check host match
		log.Printf("Checking route: host=%v,path_prefix=%v -> upstream=%v\n", route.Match.Host, route.Match.PathPrefix, route.Upstream)
		if route.Match.Host != "" && strings.EqualFold(route.Match.Host, host) {
			return url.Parse(route.Upstream)
		}
		//check path prefix match
		if route.Match.PathPrefix != "" && strings.HasPrefix(path, route.Match.PathPrefix) {
			return url.Parse(route.Upstream)
		}
	}
	return nil, errors.New("no matching upstream found")

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
