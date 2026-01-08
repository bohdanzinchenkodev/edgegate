package main

import (
	"context"
	"edgegate/filewatcher"
	"flag"
	"log"
)

func main() {
	/*listenAddr := flag.String("listen", ":8080", "listen address")
	upstreamURL := flag.String("upstream", "http://localhost:8000", "upstream base URL")*/
	configPath := flag.String("config_path", "./edgegate.yaml", "config path")

	watcher := filewatcher.NewFileWatcher(*configPath)
	ctx, cancel := context.WithCancel(context.Background())
	go watcher.Watch(ctx)
	defer cancel()

	go func() {

		for {
			select {
			case err := <-watcher.ErrorCh:
				log.Println(err)
			case filedata := <-watcher.BytesCh:
				log.Printf("new file data %s\n", filedata)
			default:

			}
		}
	}()
	select {}

	/*upstream, _ := url.Parse(*upstreamURL)

	proxy := egproxy.NewProxy(upstream)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		proxy.ServeHTTP(writer, request)
	})
	log.Println("listening on :8080 ->", upstream.String())
	log.Fatal(http.ListenAndServe(*listenAddr, mux))*/
}
