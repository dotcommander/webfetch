package webfetch

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRenderProxyListensOnlyOnLoopback(t *testing.T) {
	t.Parallel()
	proxy := newTestRenderProxy(t, NewClient(ClientConfig{AllowPrivateNetworks: true}), newProxyBudget(0, 0, nil))
	proxyURL, err := url.Parse(proxy.URL())
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	if proxyURL.Hostname() != "127.0.0.1" {
		t.Fatalf("proxy hostname = %q, want 127.0.0.1", proxyURL.Hostname())
	}
}

func TestRenderProxyForwardsHTTPThroughVettedClient(t *testing.T) {
	t.Parallel()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Proxy-Connection") != "" {
			t.Fatal("origin received proxy-only header")
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "proxied")
	}))
	t.Cleanup(origin.Close)

	proxy := newTestRenderProxy(t, NewClient(ClientConfig{AllowPrivateNetworks: true}), newProxyBudget(10, 1<<20, nil))
	client := proxyHTTPClient(t, proxy)
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read proxied response: %v", err)
	}
	if string(body) != "proxied" {
		t.Fatalf("body = %q, want proxied", body)
	}
}

func TestRenderProxyRejectsPrivateTargetBeforeContact(t *testing.T) {
	t.Parallel()
	var contacts atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		contacts.Add(1)
	}))
	t.Cleanup(origin.Close)

	budget := newProxyBudget(10, 1<<20, nil)
	proxy := newTestRenderProxy(t, NewClient(ClientConfig{}), budget)
	resp, err := proxyHTTPClient(t, proxy).Get(origin.URL)
	if err != nil {
		t.Fatalf("GET blocked target through proxy: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
	if contacts.Load() != 0 {
		t.Fatalf("private origin contacts = %d, want 0", contacts.Load())
	}
	assertCodedError(t, budget.Err(), ErrorCodePrivateNetwork)
}

func TestRenderProxyCONNECTUsesValidatedIPLiteral(t *testing.T) {
	t.Parallel()
	client := NewClient(ClientConfig{})
	client.resolver = resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	})
	wantErr := errors.New("stop after vetted dial")
	var dialAddress string
	client.dialer = func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" {
			t.Fatalf("network = %q, want tcp", network)
		}
		dialAddress = address
		return nil, wantErr
	}
	proxy := newTestRenderProxy(t, client, newProxyBudget(10, 1<<20, nil))

	status := sendConnect(t, proxy, "public.example:443")
	if status != http.StatusBadGateway {
		t.Fatalf("CONNECT status = %d, want %d", status, http.StatusBadGateway)
	}
	if dialAddress != "93.184.216.34:443" {
		t.Fatalf("dial address = %q, want vetted IP literal", dialAddress)
	}
}

func TestRenderProxyCONNECTForwardsBytes(t *testing.T) {
	t.Parallel()

	upstreamListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen upstream: %v", err)
	}
	t.Cleanup(func() { _ = upstreamListener.Close() })
	upstreamDone := make(chan error, 1)
	go func() {
		conn, err := upstreamListener.Accept()
		if err != nil {
			upstreamDone <- err
			return
		}
		defer conn.Close()
		payload := make([]byte, len("ping"))
		if _, err := io.ReadFull(conn, payload); err != nil {
			upstreamDone <- err
			return
		}
		if string(payload) != "ping" {
			upstreamDone <- fmt.Errorf("upstream payload = %q, want ping", payload)
			return
		}
		_, err = io.WriteString(conn, "pong")
		upstreamDone <- err
	}()

	proxy := newTestRenderProxy(t, NewClient(ClientConfig{AllowPrivateNetworks: true}), newProxyBudget(10, 1<<20, nil))
	proxyConn, err := net.DialTimeout("tcp", proxy.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("dial render proxy: %v", err)
	}
	defer proxyConn.Close()
	_ = proxyConn.SetDeadline(time.Now().Add(2 * time.Second))
	authority := upstreamListener.Addr().String()
	if _, err := fmt.Fprintf(proxyConn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", authority, authority); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(proxyConn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("CONNECT status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	_ = resp.Body.Close()
	if _, err := io.WriteString(proxyConn, "ping"); err != nil {
		t.Fatalf("write tunnel payload: %v", err)
	}
	payload := make([]byte, len("pong"))
	if _, err := io.ReadFull(proxyConn, payload); err != nil {
		t.Fatalf("read tunnel payload: %v", err)
	}
	if string(payload) != "pong" {
		t.Fatalf("tunnel payload = %q, want pong", payload)
	}
	if err := <-upstreamDone; err != nil {
		t.Fatalf("upstream tunnel: %v", err)
	}
}

func TestRenderProxyCONNECTRejectsMixedResolution(t *testing.T) {
	t.Parallel()
	client := NewClient(ClientConfig{})
	client.resolver = resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("93.184.216.34")},
			{IP: net.ParseIP("127.0.0.1")},
		}, nil
	})
	client.dialer = func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("dialer called for mixed public/private resolution")
		return nil, nil
	}
	budget := newProxyBudget(10, 1<<20, nil)
	proxy := newTestRenderProxy(t, client, budget)

	status := sendConnect(t, proxy, "public.example:443")
	if status != http.StatusBadGateway {
		t.Fatalf("CONNECT status = %d, want %d", status, http.StatusBadGateway)
	}
	assertCodedError(t, budget.Err(), ErrorCodePrivateNetwork)
}

func TestRenderProxyEnforcesRequestBudget(t *testing.T) {
	t.Parallel()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(origin.Close)
	budget := newProxyBudget(1, 1<<20, nil)
	proxy := newTestRenderProxy(t, NewClient(ClientConfig{AllowPrivateNetworks: true}), budget)
	client := proxyHTTPClient(t, proxy)

	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	_ = resp.Body.Close()
	resp, err = client.Get(origin.URL)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want %d", resp.StatusCode, http.StatusTooManyRequests)
	}
	assertCodedError(t, budget.Err(), ErrorCodeRenderBudget)
}

func TestRenderProxyEnforcesNetworkBudget(t *testing.T) {
	t.Parallel()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 128))
	}))
	t.Cleanup(origin.Close)
	budget := newProxyBudget(10, 16, nil)
	proxy := newTestRenderProxy(t, NewClient(ClientConfig{AllowPrivateNetworks: true}), budget)
	resp, err := proxyHTTPClient(t, proxy).Get(origin.URL)
	if err == nil {
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	assertCodedError(t, budget.Err(), ErrorCodeRenderBudget)
}

func TestRenderProxyCountsRequestNetworkBytes(t *testing.T) {
	t.Parallel()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(origin.Close)
	budget := newProxyBudget(10, 16, nil)
	proxy := newTestRenderProxy(t, NewClient(ClientConfig{AllowPrivateNetworks: true}), budget)
	req, err := http.NewRequest(http.MethodPost, origin.URL, strings.NewReader(strings.Repeat("x", 128)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := proxyHTTPClient(t, proxy).Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	assertCodedError(t, budget.Err(), ErrorCodeRenderBudget)
}

func TestProxyBudgetMapsGenericFailureToRenderFailed(t *testing.T) {
	t.Parallel()
	budget := newProxyBudget(1, 1, nil)
	assertCodedError(t, budget.fail(errors.New("proxy failure")), ErrorCodeRenderFailed)
}

func newTestRenderProxy(t *testing.T, client *Client, budget *proxyBudget) *renderProxy {
	t.Helper()
	proxy, err := startRenderProxy(client, budget)
	if err != nil {
		t.Fatalf("startRenderProxy: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := proxy.Close(ctx); err != nil {
			t.Errorf("close render proxy: %v", err)
		}
	})
	return proxy
}

func proxyHTTPClient(t *testing.T, proxy *renderProxy) *http.Client {
	t.Helper()
	proxyURL, err := url.Parse(proxy.URL())
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 2 * time.Second}
}

func sendConnect(t *testing.T, proxy *renderProxy, authority string) int {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxy.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("dial render proxy: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, "CONNECT "+authority+" HTTP/1.1\r\nHost: "+authority+"\r\n\r\n"); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func assertCodedError(t *testing.T, err error, code string) {
	t.Helper()
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != code || coded.Suggestion == "" {
		t.Fatalf("error = %v, coded = %+v, want code %q", err, coded, code)
	}
}
