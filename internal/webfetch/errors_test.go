package webfetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateTargetClassifiesErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		code string
	}{
		{name: "empty", raw: "", code: ErrorCodeInvalidURL},
		{name: "scheme", raw: "file:///tmp/example", code: ErrorCodeInvalidURL},
		{name: "private", raw: "http://127.0.0.1:8080", code: ErrorCodePrivateNetwork},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := validateTarget(context.Background(), tt.raw, false)
			var coded *CodedError
			if !errors.As(err, &coded) {
				t.Fatalf("error = %T, want *CodedError", err)
			}
			if coded.Code != tt.code || coded.Suggestion == "" {
				t.Fatalf("coded error = %+v", coded)
			}
		})
	}
}

func TestCodedErrorPreservesSentinel(t *testing.T) {
	t.Parallel()

	service := NewService(Config{AllowPrivateNetworks: true})
	_, err := service.Fetch(context.Background(), FetchRequest{URL: "http://example.com", Reader: "unknown"})
	if !errors.Is(err, ErrUnsupportedReader) {
		t.Fatalf("error = %v, want ErrUnsupportedReader", err)
	}
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != ErrorCodeUnsupportedReader {
		t.Fatalf("coded error = %+v", coded)
	}
}

func TestSearchMissingAPIKeyHasRecoveryMetadata(t *testing.T) {
	t.Parallel()

	service := NewService(Config{AllowPrivateNetworks: true})
	_, err := service.Search(context.Background(), SearchRequest{Query: "go"})
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != ErrorCodeMissingAPIKey || coded.Suggestion == "" {
		t.Fatalf("coded error = %+v", coded)
	}
}

func TestResponseTooLargeHasRecoveryMetadata(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("x", 32)))
	}))
	t.Cleanup(server.Close)

	service := NewService(Config{AllowPrivateNetworks: true, MaxBodyBytes: 8})
	_, err := service.Fetch(context.Background(), FetchRequest{URL: server.URL, Raw: true})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != ErrorCodeResponseTooLarge || coded.Suggestion == "" {
		t.Fatalf("coded error = %+v", coded)
	}
}
