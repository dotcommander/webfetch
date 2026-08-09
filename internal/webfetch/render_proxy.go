package webfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const renderProxyTunnelIdleTimeout = 30 * time.Second

type proxyBudget struct {
	mu              sync.Mutex
	maxRequests     int
	maxNetworkBytes int64
	requests        int
	networkBytes    int64
	err             error
	cancel          context.CancelFunc
}

func newProxyBudget(maxRequests int, maxNetworkBytes int64, cancel context.CancelFunc) *proxyBudget {
	return &proxyBudget{maxRequests: maxRequests, maxNetworkBytes: maxNetworkBytes, cancel: cancel}
}

func (b *proxyBudget) addRequest() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.requests++
	if b.maxRequests > 0 && b.requests > b.maxRequests {
		return b.failLocked(fmt.Errorf("browser request budget exceeded: %d requests exceeds %d", b.requests, b.maxRequests))
	}
	return nil
}

func (b *proxyBudget) addNetworkBytes(n int64) error {
	if n <= 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.networkBytes += n
	if b.maxNetworkBytes > 0 && b.networkBytes > b.maxNetworkBytes {
		return b.failLocked(fmt.Errorf("browser network budget exceeded: %d bytes exceeds %d", b.networkBytes, b.maxNetworkBytes))
	}
	return nil
}

func (b *proxyBudget) fail(err error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	var coded *CodedError
	if errors.As(err, &coded) {
		b.err = err
	} else {
		b.err = newCodedError(fmt.Errorf("browser proxy: %w", err), ErrorCodeRenderFailed, "retry the page or use static Defuddle extraction")
	}
	if b.cancel != nil {
		b.cancel()
	}
	return b.err
}

func (b *proxyBudget) failLocked(err error) error {
	b.err = newCodedError(err, ErrorCodeRenderBudget, "reduce page activity or increase the server render budget")
	if b.cancel != nil {
		b.cancel()
	}
	return b.err
}

func (b *proxyBudget) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

type renderProxy struct {
	listener  net.Listener
	server    *http.Server
	client    *Client
	budget    *proxyBudget
	closeOnce sync.Once
}

func startRenderProxy(client *Client, budget *proxyBudget) (*renderProxy, error) {
	if client == nil {
		return nil, errors.New("render proxy requires an HTTP client")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for render proxy: %w", err)
	}
	p := &renderProxy{listener: listener, client: client, budget: budget}
	p.server = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       renderProxyTunnelIdleTimeout,
		MaxHeaderBytes:    64 << 10,
	}
	go func() {
		_ = p.server.Serve(listener)
	}()
	return p, nil
}

func (p *renderProxy) URL() string {
	return "http://" + p.listener.Addr().String()
}

func (p *renderProxy) Close(ctx context.Context) error {
	var closeErr error
	p.closeOnce.Do(func() {
		closeErr = p.server.Shutdown(ctx)
		if errors.Is(closeErr, context.Canceled) || errors.Is(closeErr, context.DeadlineExceeded) {
			_ = p.server.Close()
		}
	})
	return closeErr
}

func (p *renderProxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if p.budget != nil {
		if err := p.budget.addRequest(); err != nil {
			http.Error(w, "render request budget exceeded", http.StatusTooManyRequests)
			return
		}
	}
	if req.Method == http.MethodConnect {
		p.serveConnect(w, req)
		return
	}
	p.serveHTTP(w, req)
}

func (p *renderProxy) serveHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL == nil || !req.URL.IsAbs() {
		p.reject(w, newCodedError(errors.New("proxy request URL must be absolute"), ErrorCodeInvalidURL, "use an absolute http or https URL"))
		return
	}
	if err := validateURL(req.Context(), req.URL, p.client.allowPrivateNetworks); err != nil {
		p.reject(w, err)
		return
	}

	outReq := req.Clone(req.Context())
	outReq.RequestURI = ""
	removeHopHeaders(outReq.Header)
	if outReq.Body != nil {
		outReq.Body = &budgetReadCloser{ReadCloser: outReq.Body, budget: p.budget}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = p.client.dialContext
	transport.ForceAttemptHTTP2 = true
	defer transport.CloseIdleConnections()

	resp, err := transport.RoundTrip(outReq)
	if err != nil {
		p.reject(w, err)
		return
	}
	defer resp.Body.Close()
	removeHopHeaders(resp.Header)
	copyHeaderMap(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, &budgetReader{reader: resp.Body, budget: p.budget})
}

func (p *renderProxy) serveConnect(w http.ResponseWriter, req *http.Request) {
	authority := strings.TrimSpace(req.Host)
	if authority == "" {
		p.reject(w, newCodedError(errors.New("CONNECT target is required"), ErrorCodeInvalidURL, "provide a valid host and port"))
		return
	}
	if _, _, err := net.SplitHostPort(authority); err != nil {
		authority = net.JoinHostPort(strings.Trim(authority, "[]"), "443")
	}
	target := &url.URL{Scheme: "https", Host: authority}
	if err := validateURL(req.Context(), target, p.client.allowPrivateNetworks); err != nil {
		p.reject(w, err)
		return
	}
	upstream, err := p.client.dialContext(req.Context(), "tcp", authority)
	if err != nil {
		p.reject(w, err)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		p.reject(w, errors.New("render proxy does not support connection hijacking"))
		return
	}
	downstream, rw, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		p.reject(w, err)
		return
	}
	defer downstream.Close()
	defer upstream.Close()
	if _, err := rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := rw.Flush(); err != nil {
		return
	}

	_ = downstream.SetDeadline(time.Now().Add(renderProxyTunnelIdleTimeout))
	_ = upstream.SetDeadline(time.Now().Add(renderProxyTunnelIdleTimeout))
	errCh := make(chan error, 2)
	go proxyTunnelCopy(errCh, upstream, io.MultiReader(rw.Reader, downstream), p.budget)
	go proxyTunnelCopy(errCh, downstream, upstream, p.budget)
	<-errCh
}

func proxyTunnelCopy(errCh chan<- error, dst io.Writer, src io.Reader, budget *proxyBudget) {
	_, err := io.Copy(dst, &budgetReader{reader: src, budget: budget})
	errCh <- err
}

func (p *renderProxy) reject(w http.ResponseWriter, err error) {
	if p.budget != nil {
		var coded *CodedError
		if errors.As(err, &coded) {
			p.budget.fail(err)
		}
	}
	http.Error(w, "render proxy rejected request", http.StatusBadGateway)
}

type budgetReader struct {
	reader io.Reader
	budget *proxyBudget
}

func (r *budgetReader) Read(buf []byte) (int, error) {
	n, err := r.reader.Read(buf)
	if n > 0 && r.budget != nil {
		if budgetErr := r.budget.addNetworkBytes(int64(n)); budgetErr != nil {
			return n, budgetErr
		}
	}
	return n, err
}

type budgetReadCloser struct {
	io.ReadCloser
	budget *proxyBudget
}

func (r *budgetReadCloser) Read(buf []byte) (int, error) {
	n, err := r.ReadCloser.Read(buf)
	if n > 0 && r.budget != nil {
		if budgetErr := r.budget.addNetworkBytes(int64(n)); budgetErr != nil {
			return n, budgetErr
		}
	}
	return n, err
}

func removeHopHeaders(header http.Header) {
	for _, name := range strings.Split(header.Get("Connection"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			header.Del(name)
		}
	}
	for _, name := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		header.Del(name)
	}
}

func copyHeaderMap(dst, src http.Header) {
	for name, values := range src {
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

var _ http.Handler = (*renderProxy)(nil)
