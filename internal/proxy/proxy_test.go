package egproxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

const fakeHopHeader = "X-Fake-Hop-Header-For-Test"

func init() {
	hopHeaders = append(hopHeaders, fakeHopHeader)
}

func TestReverseProxy(t *testing.T) {
	const backendResponse = "I am the backend"
	const backendStatus = 404
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.FormValue("mode") == "hangup" {
			c, _, _ := w.(http.Hijacker).Hijack()
			c.Close()
			return
		}
		if len(r.TransferEncoding) > 0 {
			t.Errorf("backend got unexpected TransferEncoding: %v", r.TransferEncoding)
		}
		if r.Header.Get("X-Forwarded-For") == "" {
			t.Errorf("didn't get X-Forwarded-For header")
		}
		if c := r.Header.Get("Connection"); c != "" {
			t.Errorf("handler got Connection header value %q", c)
		}
		if c := r.Header.Get("Te"); c != "trailers" {
			t.Errorf("handler got Te header value %q; want 'trailers'", c)
		}
		if c := r.Header.Get("Upgrade"); c != "" {
			t.Errorf("handler got Upgrade header value %q", c)
		}
		if c := r.Header.Get("Proxy-Connection"); c != "" {
			t.Errorf("handler got Proxy-Connection header value %q", c)
		}
		if g, e := r.Host, "some-name"; g != e {
			t.Errorf("backend got Host header %q, want %q", g, e)
		}
		w.Header().Set("Trailers", "not a special header field name")
		w.Header().Set("Trailer", "X-Trailer")
		w.Header().Set("X-Foo", "bar")
		w.Header().Set("Upgrade", "foo")
		w.Header().Set(fakeHopHeader, "foo")
		w.Header().Add("X-Multi-Value", "foo")
		w.Header().Add("X-Multi-Value", "bar")
		http.SetCookie(w, &http.Cookie{Name: "flavor", Value: "chocolateChip"})
		w.WriteHeader(backendStatus)
		w.Write([]byte(backendResponse))
		w.Header().Set("X-Trailer", "trailer_value")
		w.Header().Set(http.TrailerPrefix+"X-Unannounced-Trailer", "unannounced_trailer_value")
	}))
	defer backend.Close()
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyHandler := NewProxy(backendURL)
	proxyHandler.ErrorHandler = func(wr http.ResponseWriter, _ *http.Request, er error) {
		wr.WriteHeader(http.StatusBadGateway)
	}
	frontend := httptest.NewServer(proxyHandler)
	defer frontend.Close()
	frontendClient := frontend.Client()

	getReq, _ := http.NewRequest("GET", frontend.URL, nil)
	getReq.Host = "some-name"
	getReq.Header.Set("Connection", "close, TE")
	getReq.Header.Add("Te", "foo")
	getReq.Header.Add("Te", "bar, trailers")
	getReq.Header.Set("Proxy-Connection", "should be deleted")
	getReq.Header.Set("Upgrade", "foo")
	getReq.Close = true
	res, err := frontendClient.Do(getReq)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if g, e := res.StatusCode, backendStatus; g != e {
		t.Errorf("got res.StatusCode %d; expected %d", g, e)
	}
	if g, e := res.Header.Get("X-Foo"), "bar"; g != e {
		t.Errorf("got X-Foo %q; expected %q", g, e)
	}
	if c := res.Header.Get(fakeHopHeader); c != "" {
		t.Errorf("got %s header value %q", fakeHopHeader, c)
	}
	if g, e := res.Header.Get("Trailers"), "not a special header field name"; g != e {
		t.Errorf("header Trailers = %q; want %q", g, e)
	}
	if g, e := len(res.Header["X-Multi-Value"]), 2; g != e {
		t.Errorf("got %d X-Multi-Value header values; expected %d", g, e)
	}
	if g, e := len(res.Header["Set-Cookie"]), 1; g != e {
		t.Fatalf("got %d SetCookies, want %d", g, e)
	}
	if g, e := res.Trailer, (http.Header{"X-Trailer": nil}); !reflect.DeepEqual(g, e) {
		t.Errorf("before reading body, Trailer = %#v; want %#v", g, e)
	}
	if cookie := res.Cookies()[0]; cookie.Name != "flavor" {
		t.Errorf("unexpected cookie %q", cookie.Name)
	}
	bodyBytes, _ := io.ReadAll(res.Body)
	if g, e := string(bodyBytes), backendResponse; g != e {
		t.Errorf("got body %q; expected %q", g, e)
	}
	if g, e := res.Trailer.Get("X-Trailer"), "trailer_value"; g != e {
		t.Errorf("Trailer(X-Trailer) = %q ; want %q", g, e)
	}
	if g, e := res.Trailer.Get("X-Unannounced-Trailer"), "unannounced_trailer_value"; g != e {
		t.Errorf("Trailer(X-Unannounced-Trailer) = %q ; want %q", g, e)
	}
	res.Body.Close()

	// Test that a backend failing to be reached or one which doesn't return
	// a response results in a StatusBadGateway.
	getReq, _ = http.NewRequest("GET", frontend.URL+"/?mode=hangup", nil)
	getReq.Close = true
	res, err = frontendClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Errorf("request to bad proxy = %v; want 502 StatusBadGateway", res.Status)
	}
}
func TestReverseProxyStripHeadersPresentInConnection(t *testing.T) {
	const fakeConnectionToken = "X-Fake-Connection-Token"
	const backendResponse = "I am the backend"

	// someConnHeader is some arbitrary header to be declared as a hop-by-hop header
	// in the Request's Connection header.
	const someConnHeader = "X-Some-Conn-Header"

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c := r.Header.Get("Connection"); c != "" {
			t.Errorf("handler got header %q = %q; want empty", "Connection", c)
		}
		if c := r.Header.Get(fakeConnectionToken); c != "" {
			t.Errorf("handler got header %q = %q; want empty", fakeConnectionToken, c)
		}
		if c := r.Header.Get(someConnHeader); c != "" {
			t.Errorf("handler got header %q = %q; want empty", someConnHeader, c)
		}
		w.Header().Add("Connection", "Upgrade, "+fakeConnectionToken)
		w.Header().Add("Connection", someConnHeader)
		w.Header().Set(someConnHeader, "should be deleted")
		w.Header().Set(fakeConnectionToken, "should be deleted")
		io.WriteString(w, backendResponse)
	}))
	defer backend.Close()
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyHandler := NewProxy(backendURL)
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHandler.ServeHTTP(w, r)
		if c := r.Header.Get(someConnHeader); c != "should be deleted" {
			t.Errorf("handler modified header %q = %q; want %q", someConnHeader, c, "should be deleted")
		}
		if c := r.Header.Get(fakeConnectionToken); c != "should be deleted" {
			t.Errorf("handler modified header %q = %q; want %q", fakeConnectionToken, c, "should be deleted")
		}
		c := r.Header["Connection"]
		var cf []string
		for _, f := range c {
			for sf := range strings.SplitSeq(f, ",") {
				if sf = strings.TrimSpace(sf); sf != "" {
					cf = append(cf, sf)
				}
			}
		}
		slices.Sort(cf)
		expectedValues := []string{"Upgrade", someConnHeader, fakeConnectionToken}
		slices.Sort(expectedValues)
		if !slices.Equal(cf, expectedValues) {
			t.Errorf("handler modified header %q = %q; want %q", "Connection", cf, expectedValues)
		}
	}))
	defer frontend.Close()

	getReq, _ := http.NewRequest("GET", frontend.URL, nil)
	getReq.Header.Add("Connection", "Upgrade, "+fakeConnectionToken)
	getReq.Header.Add("Connection", someConnHeader)
	getReq.Header.Set(someConnHeader, "should be deleted")
	getReq.Header.Set(fakeConnectionToken, "should be deleted")
	res, err := frontend.Client().Do(getReq)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer res.Body.Close()
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if got, want := string(bodyBytes), backendResponse; got != want {
		t.Errorf("got body %q; want %q", got, want)
	}
	if c := res.Header.Get("Connection"); c != "" {
		t.Errorf("handler got header %q = %q; want empty", "Connection", c)
	}
	if c := res.Header.Get(someConnHeader); c != "" {
		t.Errorf("handler got header %q = %q; want empty", someConnHeader, c)
	}
	if c := res.Header.Get(fakeConnectionToken); c != "" {
		t.Errorf("handler got header %q = %q; want empty", fakeConnectionToken, c)
	}
}

func TestReverseProxyStripEmptyConnection(t *testing.T) {
	// See Issue 46313.
	const backendResponse = "I am the backend"

	// someConnHeader is some arbitrary header to be declared as a hop-by-hop header
	// in the Request's Connection header.
	const someConnHeader = "X-Some-Conn-Header"

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c := r.Header.Values("Connection"); len(c) != 0 {
			t.Errorf("handler got header %q = %v; want empty", "Connection", c)
		}
		if c := r.Header.Get(someConnHeader); c != "" {
			t.Errorf("handler got header %q = %q; want empty", someConnHeader, c)
		}
		w.Header().Add("Connection", "")
		w.Header().Add("Connection", someConnHeader)
		w.Header().Set(someConnHeader, "should be deleted")
		io.WriteString(w, backendResponse)
	}))
	defer backend.Close()
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyHandler := NewProxy(backendURL)
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHandler.ServeHTTP(w, r)
		if c := r.Header.Get(someConnHeader); c != "should be deleted" {
			t.Errorf("handler modified header %q = %q; want %q", someConnHeader, c, "should be deleted")
		}
	}))
	defer frontend.Close()

	getReq, _ := http.NewRequest("GET", frontend.URL, nil)
	getReq.Header.Add("Connection", "")
	getReq.Header.Add("Connection", someConnHeader)
	getReq.Header.Set(someConnHeader, "should be deleted")
	res, err := frontend.Client().Do(getReq)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer res.Body.Close()
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if got, want := string(bodyBytes), backendResponse; got != want {
		t.Errorf("got body %q; want %q", got, want)
	}
	if c := res.Header.Get("Connection"); c != "" {
		t.Errorf("handler got header %q = %q; want empty", "Connection", c)
	}
	if c := res.Header.Get(someConnHeader); c != "" {
		t.Errorf("handler got header %q = %q; want empty", someConnHeader, c)
	}
}

func TestXForwardedFor(t *testing.T) {
	const prevForwardedFor = "client ip"
	const backendResponse = "I am the backend"
	const backendStatus = 404
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-For") == "" {
			t.Errorf("didn't get X-Forwarded-For header")
		}
		if !strings.Contains(r.Header.Get("X-Forwarded-For"), prevForwardedFor) {
			t.Errorf("X-Forwarded-For didn't contain prior data")
		}
		w.WriteHeader(backendStatus)
		w.Write([]byte(backendResponse))
	}))
	defer backend.Close()
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyHandler := NewProxy(backendURL)
	frontend := httptest.NewServer(proxyHandler)
	defer frontend.Close()

	getReq, _ := http.NewRequest("GET", frontend.URL, nil)
	getReq.Header.Set("Connection", "close")
	getReq.Header.Set("X-Forwarded-For", prevForwardedFor)
	getReq.Close = true
	res, err := frontend.Client().Do(getReq)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer res.Body.Close()
	if g, e := res.StatusCode, backendStatus; g != e {
		t.Errorf("got res.StatusCode %d; expected %d", g, e)
	}
	bodyBytes, _ := io.ReadAll(res.Body)
	if g, e := string(bodyBytes), backendResponse; g != e {
		t.Errorf("got body %q; expected %q", g, e)
	}
}
func TestReverseProxyCancellation(t *testing.T) {
	const backendResponse = "I am the backend"

	reqInFlight := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(reqInFlight) // cause the client to cancel its request

		select {
		case <-time.After(10 * time.Second):
			// Note: this should only happen in broken implementations, and the
			// request context cancellation should be instantaneous.
			t.Error("handler never observed request context cancellation")
			return
		case <-r.Context().Done():
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(backendResponse))
	}))

	defer backend.Close()

	backend.Config.ErrorLog = log.New(io.Discard, "", 0)

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}

	proxyHandler := NewProxy(backendURL)

	proxyHandler.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, er error) {
		w.WriteHeader(http.StatusBadGateway)
	}

	frontend := httptest.NewServer(proxyHandler)
	defer frontend.Close()
	frontendClient := frontend.Client()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	getReq, _ := http.NewRequestWithContext(ctx, "GET", frontend.URL, nil)
	go func() {
		<-reqInFlight
		cancel()
	}()
	res, err := frontendClient.Do(getReq)
	if res != nil {
		t.Errorf("got response %v; want nil", res.Status)
	}
	if err == nil {
		// This should be an error like:
		// Get "http://127.0.0.1:58079": read tcp 127.0.0.1:58079:
		//    use of closed network connection
		t.Error("Server.Client().Do() returned nil error; want non-nil error")
	}
}

func TestReverseProxyPost_ForwardsRequestBody(t *testing.T) {
	const backendResponse = "I am the backend"
	requestBody := bytes.Repeat([]byte("a"), 1<<20)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slurp, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("backend body read = %v", err)
		}
		if len(slurp) != len(requestBody) {
			t.Fatalf("backend read %d request body bytes; want %d", len(slurp), len(requestBody))
		}
		if !bytes.Equal(slurp, requestBody) {
			t.Fatalf("backend read wrong request body")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(backendResponse))
	}))
	defer backend.Close()

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}

	frontend := httptest.NewServer(NewProxy(backendURL))
	defer frontend.Close()

	postReq, _ := http.NewRequest("POST", frontend.URL, bytes.NewReader(requestBody))
	res, err := frontend.Client().Do(postReq)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("got status %d; want %d", res.StatusCode, http.StatusOK)
	}
	bodyBytes, _ := io.ReadAll(res.Body)
	if got, want := string(bodyBytes), backendResponse; got != want {
		t.Fatalf("got body %q; want %q", got, want)
	}
}

func TestReverseProxyErrorHandler_DefaultReturnsBadGateway(t *testing.T) {
	backendURL, _ := url.Parse("http://dummy.tld")
	proxyHandler := NewProxy(backendURL)
	proxyHandler.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("some error")
	})

	frontend := httptest.NewServer(proxyHandler)
	defer frontend.Close()

	res, err := frontend.Client().Get(frontend.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer res.Body.Close()

	if got, want := res.StatusCode, http.StatusBadGateway; got != want {
		t.Fatalf("got status %d; want %d", got, want)
	}
}

func TestReverseProxyErrorHandler_CustomHandlerIsUsed(t *testing.T) {
	backendURL, _ := url.Parse("http://dummy.tld")
	proxyHandler := NewProxy(backendURL)
	proxyHandler.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("some error")
	})
	proxyHandler.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		rw.WriteHeader(http.StatusTeapot)
	}

	frontend := httptest.NewServer(proxyHandler)
	defer frontend.Close()

	res, err := frontend.Client().Get(frontend.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer res.Body.Close()

	if got, want := res.StatusCode, http.StatusTeapot; got != want {
		t.Fatalf("got status %d; want %d", got, want)
	}
}

func TestReverseProxyServeHTTP_DoesNotMutateOriginalURL(t *testing.T) {
	backendURL, _ := url.Parse("http://dummy.tld")
	proxyHandler := NewProxy(backendURL)
	proxyHandler.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/some-path?q=1", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	before := req.URL.String()

	proxyHandler.ServeHTTP(httptest.NewRecorder(), req)

	after := req.URL.String()
	if got, want := after, before; got != want {
		t.Fatalf("request URL mutated: got %q; want %q", got, want)
	}
}

func TestReverseProxyServeHTTP_DoesNotMutateOriginalHeaders(t *testing.T) {
	backendURL, _ := url.Parse("http://dummy.tld")
	proxyHandler := NewProxy(backendURL)
	proxyHandler.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("From-Test") != "1" {
			t.Fatalf("outbound request missing original header")
		}
		if req.Header.Get("X-Forwarded-For") == "" {
			t.Fatalf("outbound request missing X-Forwarded-For")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.RemoteAddr = "1.2.3.4:56789"
	req.Header.Set("From-Test", "1")

	proxyHandler.ServeHTTP(httptest.NewRecorder(), req)

	if got := req.Header.Get("From-Test"); got != "1" {
		t.Fatalf("original request header mutated: got %q; want %q", got, "1")
	}
	if got := req.Header.Get("X-Forwarded-For"); got != "" {
		t.Fatalf("original request header mutated: X-Forwarded-For = %q; want empty", got)
	}
}

func TestReverseProxyOutbound_NilBodyForGet(t *testing.T) {
	backendURL, _ := url.Parse("http://dummy.tld")
	var bodyWasNilOrNoBody bool
	proxyHandler := NewProxy(backendURL)
	proxyHandler.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		bodyWasNilOrNoBody = req.Body == nil || req.Body == http.NoBody
		return nil, errors.New("force bad gateway")
	})

	frontend := httptest.NewServer(proxyHandler)
	defer frontend.Close()

	res, err := frontend.Client().Get(frontend.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer res.Body.Close()

	if got, want := res.StatusCode, http.StatusBadGateway; got != want {
		t.Fatalf("got status %d; want %d", got, want)
	}
	if !bodyWasNilOrNoBody {
		t.Fatalf("expected GET outbound body to be nil or http.NoBody")
	}
}

func TestReverseProxyWebSocket_UpgradeAndBidirectionalData(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if getUpgradeHeader(r.Header) != "websocket" {
			t.Fatalf("unexpected backend upgrade header: %q", r.Header.Get("Upgrade"))
		}

		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatalf("hijack backend conn: %v", err)
		}
		defer conn.Close()

		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: upgrade\r\nUpgrade: websocket\r\n\r\n")

		scanner := bufio.NewScanner(conn)
		if !scanner.Scan() {
			t.Fatalf("backend failed to read from client: %v", scanner.Err())
		}
		_, _ = fmt.Fprintf(conn, "backend got %q\n", scanner.Text())
	}))
	defer backend.Close()

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}

	frontend := httptest.NewServer(NewProxy(backendURL))
	defer frontend.Close()

	req, _ := http.NewRequest(http.MethodGet, frontend.URL, nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	res, err := frontend.Client().Do(req)
	if err != nil {
		t.Fatalf("upgrade request failed: %v", err)
	}
	defer res.Body.Close()

	if got, want := res.StatusCode, http.StatusSwitchingProtocols; got != want {
		t.Fatalf("got status %d; want %d", got, want)
	}

	rwc, ok := res.Body.(io.ReadWriteCloser)
	if !ok {
		t.Fatalf("response body type %T does not implement io.ReadWriteCloser", res.Body)
	}

	_, err = io.WriteString(rwc, "Hello\n")
	if err != nil {
		t.Fatalf("write to upgraded connection: %v", err)
	}

	scanner := bufio.NewScanner(rwc)
	if !scanner.Scan() {
		t.Fatalf("read from upgraded connection: %v", scanner.Err())
	}

	if got, want := scanner.Text(), `backend got "Hello"`; got != want {
		t.Fatalf("got %q; want %q", got, want)
	}
}

func TestReverseProxyWebSocket_ClientClosePropagatesToBackend(t *testing.T) {
	backendDone := make(chan struct{})

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if getUpgradeHeader(r.Header) != "websocket" {
			t.Fatalf("unexpected backend upgrade header: %q", r.Header.Get("Upgrade"))
		}

		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatalf("hijack backend conn: %v", err)
		}
		defer conn.Close()

		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: upgrade\r\nUpgrade: websocket\r\n\r\n")

		_, _ = io.Copy(io.Discard, conn)
		close(backendDone)
	}))
	defer backend.Close()

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}

	frontend := httptest.NewServer(NewProxy(backendURL))
	defer frontend.Close()

	req, _ := http.NewRequest(http.MethodGet, frontend.URL, nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	res, err := frontend.Client().Do(req)
	if err != nil {
		t.Fatalf("upgrade request failed: %v", err)
	}

	if got, want := res.StatusCode, http.StatusSwitchingProtocols; got != want {
		res.Body.Close()
		t.Fatalf("got status %d; want %d", got, want)
	}

	rwc, ok := res.Body.(io.ReadWriteCloser)
	if !ok {
		res.Body.Close()
		t.Fatalf("response body type %T does not implement io.ReadWriteCloser", res.Body)
	}

	_, _ = io.WriteString(rwc, "hello\n")
	_ = rwc.Close()

	select {
	case <-backendDone:
	case <-time.After(1 * time.Second):
		t.Fatalf("backend did not observe upgraded connection close")
	}
}
