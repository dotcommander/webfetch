package webfetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateURLRejectsUnsafeTargets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: "URL is required"},
		{name: "scheme", raw: "file:///tmp/example", want: "unsupported URL scheme"},
		{name: "credentials", raw: "https://user:pass@example.com", want: "embedded credentials"},
		{name: "loopback", raw: "http://127.0.0.1:8080", want: "private address"},
		{name: "localhost", raw: "http://localhost:8080", want: "private host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := validateTarget(context.Background(), tt.raw, false)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateTarget(%q) error = %v, want substring %q", tt.raw, err, tt.want)
			}
		})
	}
}

func TestValidateURLAllowsPrivateTargetsWhenConfigured(t *testing.T) {
	t.Parallel()
	if _, err := validateTarget(context.Background(), "http://127.0.0.1:8080/path", true); err != nil {
		t.Fatalf("validateTarget with private networks enabled: %v", err)
	}
}

func TestReadBoundedRejectsOversizedBody(t *testing.T) {
	t.Parallel()
	_, err := readBounded(strings.NewReader("12345"), 4)
	if !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("readBounded error = %v, want %v", err, errBodyTooLarge)
	}
}

func TestClientRejectsPrivateRedirect(t *testing.T) {
	t.Parallel()
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("private"))
	}))
	private.Close()

	client := NewClient(ClientConfig{})
	if _, err := client.Get(context.Background(), private.URL, nil); err == nil {
		t.Fatal("expected private target to be rejected")
	}
}

func TestClientClassifiesHTTPStatuses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code int
		want string
	}{
		{name: "unauthorized", code: http.StatusUnauthorized, want: ErrorCodeAuthentication},
		{name: "forbidden", code: http.StatusForbidden, want: ErrorCodeAuthentication},
		{name: "rate limited", code: http.StatusTooManyRequests, want: ErrorCodeRateLimited},
		{name: "generic upstream", code: http.StatusInternalServerError, want: ErrorCodeUpstreamHTTP},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(tt.code)
				_, _ = w.Write([]byte("bounded error"))
			}))
			t.Cleanup(server.Close)

			client := NewClient(ClientConfig{AllowPrivateNetworks: true})
			_, err := client.Get(context.Background(), server.URL, nil)
			var coded *CodedError
			if !errors.As(err, &coded) || coded.Code != tt.want {
				t.Fatalf("error = %v, coded = %+v, want %q", err, coded, tt.want)
			}
		})
	}
}
