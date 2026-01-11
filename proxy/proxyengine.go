package egproxy

import (
	"context"
	"edgegate/config"
	"log"
	"net/http"
	"net/url"
	"os"
)

func StartEngine(ctx context.Context, configPath string) {
	/*fw := config.NewFileWatcher(configPath)
	fw.FileChangedHandler = func(file []byte) {
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
	fw.Watch(ctx)*/
	file, err := os.ReadFile(configPath)
	if err != nil {
		log.Print(err)
		return
	}
	cfg, err := config.ParseConfig(file)
	if err != nil {
		log.Print(err)
		return
	}
	initEngine(cfg)
}
func initEngine(cfg *config.ReverseProxyConfig) {
	for _, l := range cfg.Listeners {
		serv := &http.Server{
			Addr: l.Listen,
		}
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
		go startServer(serv)
	}
	select {}
}
func startServer(serv *http.Server) {
	log.Printf("Starting server on address: %v", serv.Addr)
	err := serv.ListenAndServe()
	defer serv.Close()
	if err != nil {
		log.Print(err)
	}
}
