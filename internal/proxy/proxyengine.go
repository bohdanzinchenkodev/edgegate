package egproxy

import (
	"context"
	"edgegate/internal/config"
	"errors"
	"log"
	"net"
	"net/http"
	"reflect"
	"sync"
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
	server         *http.Server
	done           chan struct{}
	handlerWrapper *handlerWrapper
	middlewares    []HandlerMiddleware
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
				handler = pr
			} else {
				//if routes didn't change then we reuse old handler (compiledListener)
				handler = ps.handlerWrapper.current.Load().(http.Handler) //todo fix. current includes middlewares but we want to reuse compiledListener only.
			}
			// we want to reuse old middlewares if they are the same (e.g. rate limiter with the same config) to avoid unnecessary resets and state loss
			newMiddlewares, middlewaresChanged := buildNewMiddlewares(middlewares, ps.middlewares, l.Listen)
			if !routesChanged && !middlewaresChanged {
				log.Printf("[config] listener %s: no changes detected", l.Listen)
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
		upd.ps.handlerWrapper.Update(handler)
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
