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
	upstream  *url.URL
	Transport http.RoundTripper
}

func NewProxy(upstream *url.URL) *EdgeGateProxy {
	return &EdgeGateProxy{
		upstream: upstream,
	}
}

var defaultTransport = &http.Transport{
	DialContext: (&net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,

	TLSHandshakeTimeout:   5 * time.Second,
	ResponseHeaderTimeout: 10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,

	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 20,
	MaxConnsPerHost:     100,
	IdleConnTimeout:     90 * time.Second,

	DisableKeepAlives: false,
	ForceAttemptHTTP2: true,
}

func (proxy *EdgeGateProxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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
	outreq.Host = proxy.upstream.Host
	outreq.RequestURI = ""

	upgradeHeader := getUpgradeHeader(req.Header)

	removeHopByHopHeaders(outreq.Header)

	if upgradeHeader != "" {
		outreq.Header.Set("Connection", "upgrade")
		outreq.Header.Set("Upgrade", upgradeHeader)
	}
	//send request
	res, err := proxy.Transport.RoundTrip(outreq)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Print(err)
		return
	}

	if res.StatusCode == http.StatusSwitchingProtocols {
		err = upgrade(w, res, req)
		if err != nil {
			log.Print(err)
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
		log.Print(err)
	}

	err = res.Body.Close()
	if err != nil {
		log.Print(err)
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
	return err
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
