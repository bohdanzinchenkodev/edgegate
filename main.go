package main

import (
	"edgegate/ratelimit"
	"flag"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
)

func main() {
	listenAddr := flag.String("listen", ":8080", "listen address")
	upstreamURL := flag.String("upstream", "https://www.google.com", "upstream base URL")
	rlCapacity := flag.Int("rl_capacity", 10, "rate limiter capacity")
	rlRefillRate := flag.Int("rl_refill_rate", 2, "rate limiter refill rate")
	rlTokenUsageRate := flag.Int("rl_token_usage_rate", 1, "rate limiter token usage rate")
	flag.Parse()

	upstream, _ := url.Parse(*upstreamURL)
	upstream.Path = ""
	proxy := httputil.NewSingleHostReverseProxy(upstream)

	// Optional: customize the outgoing request (headers, host, etc.)
	origDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		origDirector(r)

		// Example: add a header
		r.Header.Set("X-Proxy", "edgegate")
		r.Host = upstream.Host
	}

	// Optional: inspect/adjust the upstream response
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("X-Proxy", "edgegate")
		return nil
	}

	// Optional: customize error handling (upstream down, timeouts, etc.)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error: %v", err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	rateLimiter := ratelimit.NewRateLimiter(*rlCapacity, *rlRefillRate, *rlTokenUsageRate)
	// Catch-all: everything else goes upstream
	mux.Handle("/", http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		host, _, _ := net.SplitHostPort(req.RemoteAddr)
		if !rateLimiter.AllowReq(host) {
			http.Error(rw, "too many requests", http.StatusTooManyRequests)
			return
		}
		proxy.ServeHTTP(rw, req)
	}))

	log.Println("listening on :8080 ->", upstream.String())
	log.Fatal(http.ListenAndServe(*listenAddr, mux))

}
