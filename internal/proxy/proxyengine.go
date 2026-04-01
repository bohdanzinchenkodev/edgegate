package egproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/http2"

	"edgegate/internal/config"
)

// Global state for running proxy servers.
var (
	mu      sync.Mutex
	servers = map[string]*proxyServer{}
	currCfg = config.ReverseProxyConfig{}

	startServerAsyncFn    = func(ps *proxyServer) { go startServer(ps) }
	shutdownServerAsyncFn = func(ps *proxyServer) { go shutdownServer(ps) }
	shutdownServerSyncFn  = shutdownServer
)

// proxyServer represents a running HTTP server for a specific listener, along with its configuration and state needed for dynamic updates
type proxyServer struct {
	server         *http.Server
	done           chan struct{}
	handlerWrapper *handlerWrapper
	middlewares    []HandlerMiddleware
	router         *proxyRouter
	tlsManager     *tlsManager
}
type compareConfigsResult struct {
	toStop    []*proxyServer
	toStart   []*proxyServer
	toUpdate  []*toUpdateServer
	toReplace []*toReplaceServer // Full replace required (for example, TLS toggled).
}
type toUpdateServer struct {
	ps            *proxyServer
	handler       http.Handler
	mwDiff        middlewareDiff
	newRouter     *proxyRouter
	newTlsManager *tlsManager // non-nil when TLS certs changed and need reload
}
type toReplaceServer struct {
	old *proxyServer
	new *proxyServer
}
type middlewareDiff struct {
	toStop  []HandlerMiddleware
	toStart []HandlerMiddleware
	current []HandlerMiddleware // resulting full slice to store
}

func newProxyServer(l config.Listener, pr *proxyRouter, middlewares []HandlerMiddleware, tm *tlsManager) *proxyServer {
	wrapper := newHandlerWrapper(buildHandlerWithMiddlewares(pr, middlewares))
	return &proxyServer{
		server:         &http.Server{Addr: l.Listen, Handler: wrapper},
		done:           make(chan struct{}),
		handlerWrapper: wrapper,
		middlewares:    middlewares,
		router:         pr,
		tlsManager:     tm,
	}
}

func compareConfigs(oldCfg, newCfg *config.ReverseProxyConfig) compareConfigsResult {
	result := compareConfigsResult{}

	// Build map from old config, keyed by listen address.
	oldMap := make(map[string]config.Listener, len(oldCfg.Listeners))
	for _, l := range oldCfg.Listeners {
		oldMap[l.Listen] = l
	}

	// Iterate new config: find added and updated listeners.
	for _, l := range newCfg.Listeners {
		pr, err := compileRouter(l)
		if err != nil {
			log.Print(err)
			continue
		}
		middlewares := compileMiddlewares(l)
		tlsCfg := compileTLSManager(l)
		ps, exists := servers[l.Listen]
		if !exists {
			// New listener.
			log.Printf("[config] listener %s: added", l.Listen)

			ps = newProxyServer(l, pr, middlewares, tlsCfg)
			servers[l.Listen] = ps

			result.toStart = append(result.toStart, ps)
		} else {

			// Remove from oldMap to mark as processed.
			delete(oldMap, l.Listen)

			// Conditions requiring full server replacement.
			needsReplace := (tlsCfg == nil) != (ps.tlsManager == nil) // TLS toggled on/off
			if needsReplace {
				log.Printf("[config] listener %s: replacing server", l.Listen)
				delete(servers, l.Listen)

				newPs := newProxyServer(l, pr, middlewares, tlsCfg)
				servers[l.Listen] = newPs

				result.toReplace = append(result.toReplace, &toReplaceServer{old: ps, new: newPs})
				continue
			}

			var handler http.Handler
			var newRouter *proxyRouter
			// Reuse current router when routes are unchanged.
			routesChanged := !pr.Equal(ps.router)
			if routesChanged {
				log.Printf("[config] listener %s: routes changed", l.Listen)
				handler = pr
				newRouter = pr
			} else {
				handler = ps.router
				newRouter = ps.router
			}

			// Reuse unchanged middleware instances to preserve state.
			diff := buildMiddlewareDiff(middlewares, ps.middlewares, l.Listen)
			middlewaresChanged := len(diff.toStop) > 0 || len(diff.toStart) > 0
			tlsChanged := !tlsCfg.Equal(ps.tlsManager)

			if tlsChanged {
				log.Printf("[config] listener %s: TLS certificates changed", l.Listen)
			}

			if !routesChanged && !middlewaresChanged && !tlsChanged {
				log.Printf("[config] listener %s: no changes detected", l.Listen)
				continue
			}

			log.Printf("[config] listener %s: updating (routes=%v middlewares=%v tls=%v)", l.Listen, routesChanged, middlewaresChanged, tlsChanged)

			var newTls *tlsManager
			if tlsChanged {
				newTls = tlsCfg
			}

			result.toUpdate = append(result.toUpdate, &toUpdateServer{
				ps:            ps,
				handler:       handler,
				mwDiff:        diff,
				newRouter:     newRouter,
				newTlsManager: newTls,
			})
		}
	}

	// Remaining entries in oldMap were removed.
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
		ApplyConfig(cfg)
	}

	fw.ErrorHandler = func(err error) { log.Print(err) }

	fw.Watch(ctx)
	// Watch returns when ctx is done.
	ShutdownAll()
}

func ApplyConfig(cfg *config.ReverseProxyConfig) {

	mu.Lock()
	defer mu.Unlock()

	comparisonRes := compareConfigs(&currCfg, cfg)

	for _, upd := range comparisonRes.toUpdate {
		// Start new middlewares before making handler live.
		startMiddlewares(upd.mwDiff.toStart, context.Background())
		handler := buildHandlerWithMiddlewares(upd.handler, upd.mwDiff.current)
		upd.ps.handlerWrapper.Update(handler)
		// Stop old middlewares after handler swap.
		shutdownMiddlewares(upd.mwDiff.toStop)
		upd.ps.middlewares = upd.mwDiff.current
		upd.ps.router = upd.newRouter
		// Reload TLS certs if changed.
		if upd.newTlsManager != nil {
			err := upd.ps.tlsManager.Reload(upd.newTlsManager.certs)
			if err != nil {
				log.Printf("[config] listener %s: TLS cert reload failed: %v", upd.ps.server.Addr, err)
			}
		}
	}

	for _, ps := range comparisonRes.toStart {
		// Start middlewares before server accepts requests.
		startMiddlewares(ps.middlewares, context.Background())
		startServerAsyncFn(ps)
	}

	for _, ps := range comparisonRes.toStop {
		shutdownMiddlewares(ps.middlewares)
		shutdownServerAsyncFn(ps)
	}

	// Replacement requires old server stop before new server start.
	for _, rp := range comparisonRes.toReplace {
		shutdownMiddlewares(rp.old.middlewares)
		// Sync shutdown ensures the port is released first.
		shutdownServerSyncFn(rp.old)
		startMiddlewares(rp.new.middlewares, context.Background())
		startServerAsyncFn(rp.new)
	}

	currCfg = *cfg
}

func startServer(ps *proxyServer) {
	defer close(ps.done)

	if ps.tlsManager != nil {
		log.Printf("Starting TLS server on %v", ps.server.Addr)
		err := ps.tlsManager.Reload(nil)
		if err != nil {
			log.Print(err)
			return
		}
		ln, err := net.Listen("tcp", ps.server.Addr)
		if err != nil {
			log.Print(err)
			return
		}
		tlsLn := tls.NewListener(ln, &tls.Config{
			GetCertificate: ps.tlsManager.GetCertificate,
			NextProtos:     []string{"h2", "http/1.1"},
		})
		_ = http2.ConfigureServer(ps.server, nil)
		err = ps.server.Serve(tlsLn)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Print(err)
		}
	} else {
		log.Printf("Starting server on %v", ps.server.Addr)
		err := ps.server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Print(err)
		}
	}
	log.Printf("Server on %v stopped", ps.server.Addr)
}

func shutdownServer(ps *proxyServer) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = ps.server.Shutdown(shutdownCtx)
	<-ps.done
}

func ShutdownAll() {
	mu.Lock()
	old := servers
	servers = map[string]*proxyServer{}
	// Shutdown middlewares while holding lock.
	for _, ps := range old {
		shutdownMiddlewares(ps.middlewares)
	}
	mu.Unlock()

	// Shutdown HTTP servers in parallel.
	var wg sync.WaitGroup
	wg.Add(len(old))
	for _, ps := range old {
		ps := ps
		go func() {
			shutdownServerSyncFn(ps)
			wg.Done()
		}()
	}
	wg.Wait()
}

func startMiddlewares(mws []HandlerMiddleware, ctx context.Context) {
	for _, mw := range mws {
		if lm, ok := mw.(LifecycleMiddleware); ok {
			lm.ServerStart(ctx)
		}
	}
}

func shutdownMiddlewares(mws []HandlerMiddleware) {
	for _, mw := range mws {
		if lm, ok := mw.(LifecycleMiddleware); ok {
			lm.ServerShutdown()
		}
	}
}

func stripPort(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}
