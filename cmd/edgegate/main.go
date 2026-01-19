package main

import (
	"context"
	"edgegate/internal/proxy"
	"flag"
	"os/signal"
	"syscall"
)

func main() {
	/*listenAddr := flag.String("listen", ":8080", "listen address")
	upstreamURL := flag.String("upstream", "http://localhost:8000", "upstream base URL")*/
	/*	configPath := flag.String("config_path", "./edgegate.yaml", "config path")

		watcher := config.NewFileWatcher(*configPath)
		ctx, cancel := context.WithCancel(context.Background())
		go watcher.Watch(ctx)
		defer cancel()

		file, err := os.ReadFile(*configPath)
		if err != nil {
			log.Println(err)
			return
		}
		_, err = config.ParseConfig(file)
		if err != nil {
			log.Println(err)
		}*/
	/*upstreamURL := flag.String("upstream", "https://example.com", "upstream base URL")
	upstream, _ := url.Parse(*upstreamURL)

	proxy := httputil.NewSingleHostReverseProxy(upstream)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		proxy.ServeHTTP(writer, request)
	})
	log.Println("listening on :8080 ->", upstream.String())
	log.Fatal(http.ListenAndServe(":8080", mux))*/
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	configPath := flag.String("conf", "./configs/edgegate.yaml", "config path")
	flag.Parse()
	egproxy.StartEngine(rootCtx, *configPath)
}
