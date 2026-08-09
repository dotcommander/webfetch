package webfetch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestClientRedirectHeaderPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		locationHost      string
		multiHop          bool
		wantAuthorization string
		wantSubscription  string
		wantAPIKey        string
	}{
		{name: "same origin", locationHost: "ORIGIN.EXAMPLE", wantAuthorization: "Bearer reader-secret", wantSubscription: "subscription-secret", wantAPIKey: "api-secret"},
		{name: "cross origin multi hop", locationHost: "other.example", multiHop: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			observedHeaders := make(chan http.Header, 1)
			var port string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/start":
					next := "/final"
					if tt.multiHop {
						next = "/middle"
					}
					http.Redirect(w, r, "http://"+tt.locationHost+":"+port+next, http.StatusFound)
				case "/middle":
					http.Redirect(w, r, "http://"+tt.locationHost+":"+port+"/final", http.StatusFound)
				case "/final":
					observedHeaders <- r.Header.Clone()
					_, _ = w.Write([]byte("ok"))
				default:
					t.Errorf("unexpected path %q", r.URL.Path)
				}
			}))
			t.Cleanup(server.Close)
			_, port, _ = net.SplitHostPort(server.Listener.Addr().String())

			client := testClientForServer(t, server)
			headers := make(http.Header)
			headers.Set("Authorization", "Bearer reader-secret")
			headers.Set("X-Subscription-Token", "subscription-secret")
			headers.Set("x-api-key", "api-secret")
			response, err := client.Get(context.Background(), "http://origin.example:"+port+"/start", headers)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if string(response.Body) != "ok" {
				t.Fatalf("response body = %q, want ok", response.Body)
			}
			gotHeaders := <-observedHeaders
			if got := gotHeaders.Get("Authorization"); got != tt.wantAuthorization {
				t.Fatalf("Authorization = %q, want %q", got, tt.wantAuthorization)
			}
			if got := gotHeaders.Get("X-Subscription-Token"); got != tt.wantSubscription {
				t.Fatalf("X-Subscription-Token = %q, want %q", got, tt.wantSubscription)
			}
			if got := gotHeaders.Get("x-api-key"); got != tt.wantAPIKey {
				t.Fatalf("x-api-key = %q, want %q", got, tt.wantAPIKey)
			}
		})
	}
}

func TestClientCheckRedirectStripsSensitiveHeadersAcrossStrictOrigin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		origin string
		target string
	}{
		{name: "scheme change", origin: "https://example.com/path", target: "http://example.com/path"},
		{name: "effective port change", origin: "https://example.com/path", target: "https://example.com:444/path"},
		{name: "subdomain change", origin: "https://example.com/path", target: "https://api.example.com/path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			origin, err := http.NewRequest(http.MethodGet, tt.origin, nil)
			if err != nil {
				t.Fatalf("origin request: %v", err)
			}
			target, err := http.NewRequest(http.MethodGet, tt.target, nil)
			if err != nil {
				t.Fatalf("target request: %v", err)
			}
			for _, name := range []string{"Authorization", "X-Subscription-Token", "x-api-key"} {
				target.Header.Set(name, "secret")
			}
			client := NewClient(ClientConfig{})
			t.Cleanup(client.httpClient.CloseIdleConnections)
			if err := client.checkRedirect(target, []*http.Request{origin}); err != nil {
				t.Fatalf("checkRedirect: %v", err)
			}
			for _, name := range []string{"Authorization", "X-Subscription-Token", "x-api-key"} {
				if got := target.Header.Get(name); got != "" {
					t.Fatalf("%s = %q, want stripped", name, got)
				}
			}
		})
	}
}

func TestClientRejectsUnsafeRedirect(t *testing.T) {
	t.Parallel()
	var blockedRequests atomic.Int32
	var port string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "http://127.0.0.1:"+port+"/blocked", http.StatusFound)
		case "/blocked":
			blockedRequests.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)
	_, port, _ = net.SplitHostPort(server.Listener.Addr().String())

	client := testClientForServer(t, server)
	_, err := client.Get(context.Background(), "http://origin.example:"+port+"/start", nil)
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != ErrorCodePrivateNetwork {
		t.Fatalf("Get error = %v, coded = %#v", err, coded)
	}
	if got := blockedRequests.Load(); got != 0 {
		t.Fatalf("blocked redirect requests = %d, want 0", got)
	}
}

func TestClientStopsAfterTenRedirectHopsWithoutRetrying(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	t.Cleanup(server.Close)
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())

	client := testClientForServer(t, server)
	_, err := client.Get(context.Background(), "http://origin.example:"+port+"/loop", nil)
	if !errors.Is(err, errRedirectLimit) {
		t.Fatalf("Get error = %v, want redirect limit", err)
	}
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != ErrorCodeUpstreamHTTP {
		t.Fatalf("Get error = %v, coded = %#v", err, coded)
	}
	if got, want := requests.Load(), int32(maxRedirectHops); got != want {
		t.Fatalf("requests = %d, want %d", got, want)
	}
}

func TestSameOrigin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		left  string
		right string
		want  bool
	}{
		{name: "case insensitive default port", left: "http://EXAMPLE.com", right: "http://example.COM:80", want: true},
		{name: "scheme differs", left: "http://example.com", right: "https://example.com", want: false},
		{name: "port differs", left: "https://example.com", right: "https://example.com:444", want: false},
		{name: "host differs", left: "https://example.com", right: "https://other.example", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			left, err := url.Parse(tt.left)
			if err != nil {
				t.Fatalf("parse left: %v", err)
			}
			right, err := url.Parse(tt.right)
			if err != nil {
				t.Fatalf("parse right: %v", err)
			}
			if got := sameOrigin(left, right); got != tt.want {
				t.Fatalf("sameOrigin(%q, %q) = %t, want %t", tt.left, tt.right, got, tt.want)
			}
		})
	}
}
