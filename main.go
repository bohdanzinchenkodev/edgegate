package main

import (
	egproxy "edgegate/proxy"
	"flag"
	"log"
	"net/http"
	"net/url"
)

func main() {
	listenAddr := flag.String("listen", ":8080", "listen address")
	upstreamURL := flag.String("upstream", "http://localhost:8000", "upstream base URL")
	/*rlCapacity := flag.Int("rl_capacity", 10, "rate limiter capacity")
	rlRefillRate := flag.Int("rl_refill_rate", 2, "rate limiter refill rate")
	rlTokenUsageRate := flag.Int("rl_token_usage_rate", 1, "rate limiter token usage rate")
	flag.Parse()*/

	upstream, _ := url.Parse(*upstreamURL)

	proxy := egproxy.NewProxy(upstream)
	mux := http.NewServeMux()
	mux.Handle("/", proxy)
	log.Println("listening on :8080 ->", upstream.String())
	log.Fatal(http.ListenAndServe(*listenAddr, mux))
}
