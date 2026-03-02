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

// Global state for running proxy servers
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
	toReplace []*toReplaceServer // server must be fully replaced (e.g. TLS toggled) — stop old, then start new sequentially
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

	// Build map from old config, keyed by listen address
	oldMap := make(map[string]config.Listener, len(oldCfg.Listeners))
	for _, l := range oldCfg.Listeners {
		oldMap[l.Listen] = l
	}

	// Iterate new config: find added + updated
	for _, l := range newCfg.Listeners {
		pr, err := compileRouter(l)
		if err != nil {
			log.Print(err)
			continue
		}
		middlewares := compileMiddlewares(l)
		tlsCfg := compileTlsManager(l)
		ps, exists := servers[l.Listen]
		if !exists {
			// New listener — create and mark for start
			log.Printf("[config] listener %s: added", l.Listen)

			ps = newProxyServer(l, pr, middlewares, tlsCfg)
			servers[l.Listen] = ps

			result.toStart = append(result.toStart, ps)
		} else {

			// Remove from oldMap to mark as processed
			delete(oldMap, l.Listen)

			// Conditions that require full server replacement (can't update in-place)
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
			// Check if routes changed to decide if we can reuse old router or need to replace with new one
			routesChanged := !pr.Equal(ps.router)
			if routesChanged {
				log.Printf("[config] listener %s: routes changed", l.Listen)
				handler = pr
				newRouter = pr
			} else {
				//if routes didn't change then we reuse router
				handler = ps.router
				newRouter = ps.router
			}

			// we want to reuse old middlewares if they are the same (e.g. rate limiter with the same config) to avoid unnecessary resets and state loss
			diff := buildMiddlewareDiff(middlewares, ps.middlewares, l.Listen)
			middlewaresChanged := len(diff.toStop) > 0 || len(diff.toStart) > 0
			tlsChanged := !tlsCfg.Equal(ps.tlsManager)

			if tlsChanged {
				log.Printf("[config] listener %s: TLS certificates changed", l.Listen)
			}

			if !routesChanged && !middlewaresChanged && !tlsChanged {
				log.Printf("[config] listener %s: no changes detected", l.Listen)
				continue // no changes, skip
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
		startMiddlewares(upd.mwDiff.toStart, context.Background())
		handler := buildHandlerWithMiddlewares(upd.handler, upd.mwDiff.current)
		upd.ps.handlerWrapper.Update(handler)
		// shutdown old middlewares AFTER handler is swapped
		shutdownMiddlewares(upd.mwDiff.toStop)
		upd.ps.middlewares = upd.mwDiff.current
		upd.ps.router = upd.newRouter
		// reload TLS certs if changed — new connections will use new certs
		if upd.newTlsManager != nil {
			err := upd.ps.tlsManager.Reload(upd.newTlsManager.certs)
			if err != nil {
				log.Printf("[config] listener %s: TLS cert reload failed: %v", upd.ps.server.Addr, err)
			}
		}
	}

	for _, ps := range comparisonRes.toStart {
		// start middlewares BEFORE server accepts requests
		startMiddlewares(ps.middlewares, context.Background())
		startServerAsyncFn(ps)
	}

	for _, ps := range comparisonRes.toStop {
		shutdownMiddlewares(ps.middlewares)
		shutdownServerAsyncFn(ps)
	}

	// Server replacement — must stop old server and release port before starting new one
	for _, rp := range comparisonRes.toReplace {
		shutdownMiddlewares(rp.old.middlewares)
		//sync shutdown to ensure port is released before new server starts
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

func shutdownAll() {
	mu.Lock()
	old := servers
	servers = map[string]*proxyServer{}
	// shutdown middlewares while holding lock (protects ps.middlewares access)
	for _, ps := range old {
		shutdownMiddlewares(ps.middlewares)
	}
	mu.Unlock()

	// shutdown HTTP servers in parallel (no lock needed - doesn't access middlewares)
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
