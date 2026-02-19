package egproxy

import (
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
)

var hopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

type EdgeGateProxy struct {
	upstream              *url.URL
	Prefix                string
	Transport             http.RoundTripper
	ErrorHandler          func(http.ResponseWriter, *http.Request, error)
	ReplaceHostToUpstream bool
}

func NewProxy(upstream *url.URL) *EdgeGateProxy {
	return &EdgeGateProxy{
		upstream: upstream,
	}
}

var defaultTransport http.RoundTripper = &http.Transport{
	MaxIdleConns:        1024,
	MaxIdleConnsPerHost: 1024,
	IdleConnTimeout:     90 * time.Second,
}

func (proxy *EdgeGateProxy) getErrorHandler() func(http.ResponseWriter, *http.Request, error) {
	if proxy.ErrorHandler != nil {
		return proxy.ErrorHandler
	}
	return func(w http.ResponseWriter, req *http.Request, err error) {
		w.WriteHeader(http.StatusBadGateway)
		log.Print(err)
	}
}
func (proxy *EdgeGateProxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	//log.Printf("Proxying request: %s %s%s to upstream %s\n", req.Method, req.Host, req.URL.Path, proxy.upstream)
	if proxy.Transport == nil {
		proxy.Transport = defaultTransport
	}
	//copy values from original request
	oldHost := req.Host
	ip, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		ip = req.RemoteAddr
	}
	oldScheme := "http"
	if req.TLS != nil {
		oldScheme = "https"
	}

	//clone request
	ctx := req.Context()
	outreq := req.Clone(ctx)
	if proxy.Prefix != "" {
		outreq.URL.Path = strings.TrimPrefix(outreq.URL.Path, proxy.Prefix)
	}
	if proxy.ReplaceHostToUpstream {
		outreq.Host = proxy.upstream.Host
	}
	//add x-forwarded headers
	if prior := outreq.Header.Get("X-Forwarded-For"); prior != "" {
		outreq.Header.Set("X-Forwarded-For", prior+", "+ip)
	} else {
		outreq.Header.Set("X-Forwarded-For", ip)
	}
	outreq.Header.Set("X-Forwarded-Host", oldHost)
	outreq.Header.Set("X-Forwarded-Proto", oldScheme)
	//modify request to point to upstream
	outreq.URL.Host = proxy.upstream.Host
	outreq.URL.Scheme = proxy.upstream.Scheme

	outreq.RequestURI = ""

	upgradeHeader := getUpgradeHeader(req.Header)
	outreq.Close = false
	removeHopByHopHeaders(outreq.Header)

	if httpguts.HeaderValuesContainsToken(req.Header["Te"], "trailers") {
		outreq.Header.Set("Te", "trailers")
	}

	if upgradeHeader != "" {
		outreq.Header.Set("Connection", "upgrade")
		outreq.Header.Set("Upgrade", upgradeHeader)
	}
	//send request
	//log.Printf("Outgoing request: %s %s%s to upstream %s\n", outreq.Method, outreq.Host, outreq.URL.Path, proxy.upstream)
	res, err := proxy.Transport.RoundTrip(outreq)
	errorHandler := proxy.getErrorHandler()
	if err != nil {
		errorHandler(w, req, err)
		return
	}

	if res.StatusCode == http.StatusSwitchingProtocols {
		err = upgrade(w, res, req)
		if err != nil {
			errorHandler(w, req, err)
		}
		return
	}
	//copy headers
	header := w.Header()
	copyHeader(header, res.Header, "")
	removeHopByHopHeaders(header)

	announcedTrailers := len(res.Trailer)
	if announcedTrailers > 0 {
		trailerKeys := make([]string, 0, len(res.Trailer))
		for k := range res.Trailer {
			trailerKeys = append(trailerKeys, k)
		}
		w.Header().Add("Trailer", strings.Join(trailerKeys, ", "))
	}

	//write response
	w.WriteHeader(res.StatusCode)

	isStream := isStreaming(res)
	var fl http.Flusher
	if isStream {
		if f, ok := w.(http.Flusher); ok {
			fl = f
			//flush headers
			fl.Flush()
		} else {
			isStream = false
		}
	}

	var buf []byte
	_, err = copyBuffer(w, res.Body, buf, fl, isStream)
	if err != nil {
		errorHandler(w, req, err)
		return
	}

	err = res.Body.Close()
	if err != nil {
		errorHandler(w, req, err)
		return
	}
	//write trailers
	prefix := ""
	if announcedTrailers > 0 {
		prefix = http.TrailerPrefix
	}
	copyHeader(w.Header(), res.Trailer, prefix)

}
func upgrade(w http.ResponseWriter, res *http.Response, req *http.Request) error {
	reqUpgrade := getUpgradeHeader(req.Header)
	resUpgrade := getUpgradeHeader(res.Header)
	if !strings.EqualFold(reqUpgrade, resUpgrade) {
		return errors.New("upgrade headers do not match")
	}
	backConn, ok := res.Body.(io.ReadWriteCloser)
	if !ok {
		return errors.New("switching protocols response with non-writable body")
	}
	defer backConn.Close()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return errors.New("response writer does not support hijacking")
	}
	clientConn, buff, err := hijacker.Hijack()
	if err != nil {
		return err
	}
	defer clientConn.Close()
	copyHeader(w.Header(), res.Header, "")
	res.Header = w.Header()
	res.Body = nil

	err = res.Write(buff)
	if err != nil {
		return err
	}
	err = buff.Flush()
	if err != nil {
		return err
	}
	errch := make(chan error, 1)
	go copyStream(clientConn, backConn, errch)
	go copyStream(backConn, clientConn, errch)

	err = <-errch
	if err == nil {
		err = <-errch
	}
	return nil
}
func copyStream(dst, src io.ReadWriter, errch chan error) {
	_, err := io.Copy(dst, src)
	if err != nil {
		errch <- err
		return
	}
	// user conn has reached EOF so propogate close write to backend conn
	if wc, ok := dst.(interface{ CloseWrite() error }); ok {
		errch <- wc.CloseWrite()
		return
	}
	errch <- errors.New("done copying")
}

func getUpgradeHeader(h http.Header) string {
	if !httpguts.HeaderValuesContainsToken(h["Connection"], "Upgrade") {
		return ""
	}
	return h.Get("Upgrade")
}
func copyBuffer(dst io.Writer, src io.Reader, buf []byte, fl http.Flusher, isStream bool) (written int64, err error) {
	if len(buf) == 0 {
		buf = make([]byte, 32*1024)
	}

	for {
		nr, rerr := src.Read(buf)
		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			if nw > 0 {
				written += int64(nw)
			}

			if werr != nil {
				return written, werr
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
			if isStream {
				fl.Flush()
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				rerr = nil
			}
			return written, rerr
		}
	}
}
func copyHeader(dst, src http.Header, prefix string) {
	for k, vv := range src {
		for _, v := range vv {
			key := k
			if prefix != "" {
				key = prefix + k
			}
			dst.Add(key, v)
		}
	}
}
func isStreaming(res *http.Response) bool {
	h := res.Header
	if strings.Contains(strings.ToLower(h.Get("Content-Type")), "text/event-stream") {
		return true
	}
	if res.ContentLength == -1 {
		return true
	}
	return false
}
func removeHopByHopHeaders(h http.Header) {
	for _, f := range h["Connection"] {
		for sf := range strings.SplitSeq(f, ",") {
			if sf = textproto.TrimString(sf); sf != "" {
				h.Del(sf)
			}
		}
	}
	for _, f := range hopHeaders {
		h.Del(f)
	}
}
