package egproxy

import (
	"context"
	"edgegate/config"
	"errors"
	"log"
	"net/http"
	"net/url"
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
		for _, r := range l.Routes {
			upstream, err := url.Parse(r.Upstream)
			if err != nil {
				log.Print(err)
				continue
			}
			proxy := NewProxy(upstream)
			proxy.Prefix = r.Match.PathPrefix
			proxy.ReplaceHostToUpstream = true
			mux.Handle(proxy.Prefix, proxy)
		}
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
