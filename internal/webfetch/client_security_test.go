package webfetch

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type resolverFunc func(context.Context, string) ([]net.IPAddr, error)

func (f resolverFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f(ctx, host)
}

func TestClientDialRejectsUnsafeResolution(t *testing.T) {
	t.Parallel()
	publicIP := net.ParseIP("93.184.216.34")
	privateIP := net.ParseIP("127.0.0.1")
	tests := []struct {
		name     string
		ips      []net.IPAddr
		err      error
		wantCode string
	}{
		{name: "lookup error", err: errors.New("lookup failed")},
		{name: "empty result"},
		{name: "private result", ips: []net.IPAddr{{IP: privateIP}}, wantCode: ErrorCodePrivateNetwork},
		{name: "mixed result", ips: []net.IPAddr{{IP: publicIP}, {IP: privateIP}}, wantCode: ErrorCodePrivateNetwork},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := NewClient(ClientConfig{})
			t.Cleanup(client.httpClient.CloseIdleConnections)
			var lookups atomic.Int32
			client.resolver = resolverFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
				if host != "public.example" {
					t.Fatalf("lookup host = %q, want public.example", host)
				}
				lookups.Add(1)
				return tt.ips, tt.err
			})
			client.dialer = func(context.Context, string, string) (net.Conn, error) {
				t.Fatal("dialer called for rejected resolution")
				return nil, nil
			}

			_, err := client.dialContext(context.Background(), "tcp", "public.example:443")
			var coded *CodedError
			if tt.wantCode == "" {
				if err == nil || errors.As(err, &coded) {
					t.Fatalf("dialContext error = %v, want uncoded resolution failure", err)
				}
			} else if !errors.As(err, &coded) || coded.Code != tt.wantCode {
				t.Fatalf("dialContext error = %v, coded = %#v, want code %q", err, coded, tt.wantCode)
			}
			if got := lookups.Load(); got != 1 {
				t.Fatalf("lookup calls = %d, want 1", got)
			}
		})
	}
}

func TestClientDialUsesValidatedIPLiteral(t *testing.T) {
	t.Parallel()
	client := NewClient(ClientConfig{})
	t.Cleanup(client.httpClient.CloseIdleConnections)
	var lookups atomic.Int32
	client.resolver = resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		lookups.Add(1)
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	})
	wantDialErr := errors.New("stop after inspecting address")
	var gotAddress string
	client.dialer = func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" {
			t.Fatalf("network = %q, want tcp", network)
		}
		gotAddress = address
		return nil, wantDialErr
	}

	_, err := client.dialContext(context.Background(), "tcp", "public.example:443")
	if !errors.Is(err, wantDialErr) {
		t.Fatalf("dialContext error = %v, want %v", err, wantDialErr)
	}
	if gotAddress != "93.184.216.34:443" {
		t.Fatalf("dial address = %q, want validated IP literal", gotAddress)
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("lookup calls = %d, want 1", got)
	}
}

func TestClientDialFallsBackAcrossValidatedIPLiterals(t *testing.T) {
	t.Parallel()
	client := NewClient(ClientConfig{})
	t.Cleanup(client.httpClient.CloseIdleConnections)
	client.resolver = resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("93.184.216.34")},
			{IP: net.ParseIP("93.184.216.35")},
		}, nil
	})
	wantDialErr := errors.New("first address unavailable")
	var addresses []string
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	client.dialer = func(_ context.Context, _, address string) (net.Conn, error) {
		addresses = append(addresses, address)
		if len(addresses) == 1 {
			return nil, wantDialErr
		}
		return clientConn, nil
	}

	conn, err := client.dialContext(context.Background(), "tcp", "public.example:443")
	if err != nil {
		t.Fatalf("dialContext: %v", err)
	}
	if conn == nil {
		t.Fatal("dialContext returned nil connection")
	}
	if len(addresses) != 2 || addresses[0] != "93.184.216.34:443" || addresses[1] != "93.184.216.35:443" {
		t.Fatalf("dial addresses = %v, want both validated IP literals in order", addresses)
	}
}

func TestClientAllowPrivateNetworksBypassesResolution(t *testing.T) {
	t.Parallel()
	client := NewClient(ClientConfig{AllowPrivateNetworks: true})
	t.Cleanup(client.httpClient.CloseIdleConnections)
	client.resolver = resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		t.Fatal("resolver called with private networks enabled")
		return nil, nil
	})
	wantDialErr := errors.New("stop after inspecting address")
	var gotAddress string
	client.dialer = func(_ context.Context, _ string, address string) (net.Conn, error) {
		gotAddress = address
		return nil, wantDialErr
	}

	_, err := client.dialContext(context.Background(), "tcp", "private.example:443")
	if !errors.Is(err, wantDialErr) {
		t.Fatalf("dialContext error = %v, want %v", err, wantDialErr)
	}
	if gotAddress != "private.example:443" {
		t.Fatalf("dial address = %q, want private.example:443", gotAddress)
	}
}

func TestClientTransportDisablesEnvironmentProxy(t *testing.T) {
	t.Parallel()
	client := NewClient(ClientConfig{})
	t.Cleanup(client.httpClient.CloseIdleConnections)
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.httpClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("transport proxy is configured")
	}
}

func TestClientPreservesHostAndTLSSNIWhenDialingValidatedIP(t *testing.T) {
	t.Parallel()
	serverNames := make(chan string, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "public.example" {
			t.Errorf("HTTP Host = %q, want public.example", r.Host)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	server.TLS = &tls.Config{
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			serverNames <- hello.ServerName
			return nil, nil
		},
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	client := NewClient(ClientConfig{})
	t.Cleanup(client.httpClient.CloseIdleConnections)
	client.resolver = resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	})
	client.dialer = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	transport := client.httpClient.Transport.(*http.Transport)
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // Test server certificate.

	response, err := client.Get(context.Background(), "https://public.example/", nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(response.Body) != "ok" {
		t.Fatalf("response body = %q, want ok", response.Body)
	}
	if got := <-serverNames; got != "public.example" {
		t.Fatalf("TLS SNI = %q, want public.example", got)
	}
}

func testClientForServer(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client := NewClient(ClientConfig{})
	t.Cleanup(client.httpClient.CloseIdleConnections)
	client.resolver = resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	})
	client.dialer = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	return client
}
