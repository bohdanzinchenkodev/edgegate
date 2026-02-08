package egproxy

import (
	"context"
	"edgegate/internal/config"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// Global state for running proxy servers
var (
	mu      sync.Mutex
	servers = map[string]*proxyServer{}
	currCfg = config.ReverseProxyConfig{}
)

// proxyServer represents a running HTTP server for a specific listener, along with its configuration and state needed for dynamic updates
type proxyServer struct {
	server         *http.Server
	done           chan struct{}
	handlerWrapper *handlerWrapper
	middlewares    []HandlerMiddleware
	router         *proxyRouter
}
type compareConfigsResult struct {
	toStop   []*proxyServer
	toStart  []*proxyServer
	toUpdate []*toUpdateServer
}
type toUpdateServer struct {
	ps        *proxyServer
	handler   http.Handler
	mwDiff    middlewareDiff
	newRouter *proxyRouter
}
type middlewareDiff struct {
	toStop  []HandlerMiddleware
	toStart []HandlerMiddleware
	current []HandlerMiddleware // resulting full slice to store
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
		ps, exists := servers[l.Listen]
		if !exists {
			// New listener — create and mark for start
			log.Printf("[config] listener %s: added", l.Listen)
			wrapper := newHandlerWrapper(buildHandlerWithMiddlewares(pr, middlewares))
			ps = &proxyServer{
				server:         &http.Server{Addr: l.Listen, Handler: wrapper},
				done:           make(chan struct{}),
				handlerWrapper: wrapper,
				middlewares:    middlewares,
				router:         pr,
			}
			servers[l.Listen] = ps
			result.toStart = append(result.toStart, ps)
		} else {

			// Remove from oldMap to mark as processed
			delete(oldMap, l.Listen)

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
			if !routesChanged && !middlewaresChanged {
				log.Printf("[config] listener %s: no changes detected", l.Listen)
				continue // no changes, skip
			}
			log.Printf("[config] listener %s: updating (routes=%v middlewares=%v)", l.Listen, routesChanged, middlewaresChanged)
			result.toUpdate = append(result.toUpdate, &toUpdateServer{
				ps:        ps,
				handler:   handler,
				mwDiff:    diff,
				newRouter: newRouter,
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
		for _, mw := range upd.mwDiff.toStart {
			mw.ServerStart(context.Background())
		}
		handler := buildHandlerWithMiddlewares(upd.handler, upd.mwDiff.current)
		upd.ps.handlerWrapper.Update(handler)
		// shutdown old middlewares AFTER handler is swapped
		for _, mw := range upd.mwDiff.toStop {
			mw.ServerShutdown()
		}
		upd.ps.middlewares = upd.mwDiff.current
		upd.ps.router = upd.newRouter
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
