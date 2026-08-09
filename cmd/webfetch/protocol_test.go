package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dotcommander/webfetch/internal/webfetch"
)

func TestRunProtocolSuccessRepairsArgsAndEchoesRequestID(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "go" || r.URL.Query().Get("count") != "3" {
			t.Errorf("query = %s, count = %s", r.URL.Query().Get("q"), r.URL.Query().Get("count"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":"Go","url":"https://go.dev","description":"Language"}]}}`))
	}))
	t.Cleanup(server.Close)

	service := webfetch.NewService(webfetch.Config{
		AllowPrivateNetworks: true,
		SearchEndpoint:       server.URL,
		BraveAPIKey:          "test-key",
	})
	var stdout bytes.Buffer
	err := runProtocolWithService(context.Background(), strings.NewReader("{\"tool\":\"web_search\",\"args\":\"{\\\"query\\\":\\\"go\\\",\\\"max_results\\\":3}\",\"request_id\":\"req-1\"}\n \t"), &stdout, service)
	if err != nil {
		t.Fatalf("runProtocol: %v", err)
	}
	var response webfetch.WireResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout.String())
	}
	if !response.OK || response.RequestID != "req-1" || response.Error != "" {
		t.Fatalf("response = %+v", response)
	}
	if response.Meta["provider"] != "brave" || response.Meta["requested_max_results"] != float64(3) {
		t.Fatalf("meta = %#v", response.Meta)
	}
}

func TestRunProtocolInvalidJSONIsStructured(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	err := runProtocolWithService(context.Background(), strings.NewReader("not-json"), &stdout, nil)
	var protocolErr *protocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	var response webfetch.WireResponse
	if decodeErr := json.Unmarshal(stdout.Bytes(), &response); decodeErr != nil {
		t.Fatalf("decode response: %v", decodeErr)
	}
	if response.OK || response.ErrorCode != webfetch.ErrorCodeInvalidJSON || response.Suggestion == "" {
		t.Fatalf("response = %+v", response)
	}
}

func TestRunProtocolRejectsTrailingInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{name: "second JSON value", input: `{"tool":"web_search","args":{"query":"go"}} {"tool":"web_search","args":{"query":"rust"}}`},
		{name: "trailing junk", input: `{"tool":"web_search","args":{"query":"go"}} junk`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			err := runProtocolWithService(context.Background(), strings.NewReader(tt.input), &stdout, nil)
			var protocolErr *protocolError
			if !errors.As(err, &protocolErr) {
				t.Fatalf("error = %T %v", err, err)
			}
			var response webfetch.WireResponse
			if decodeErr := json.Unmarshal(stdout.Bytes(), &response); decodeErr != nil {
				t.Fatalf("decode response: %v", decodeErr)
			}
			if response.OK || response.ErrorCode != webfetch.ErrorCodeInvalidJSON || response.Suggestion == "" {
				t.Fatalf("response = %+v", response)
			}
		})
	}
}

func TestRunProtocolNoInputIsStructured(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	err := runProtocolWithService(context.Background(), strings.NewReader(" \n\t"), &stdout, nil)
	var protocolErr *protocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	var response webfetch.WireResponse
	if decodeErr := json.Unmarshal(stdout.Bytes(), &response); decodeErr != nil {
		t.Fatalf("decode response: %v", decodeErr)
	}
	if response.OK || response.ErrorCode != webfetch.ErrorCodeNoInput || response.Suggestion == "" {
		t.Fatalf("response = %+v", response)
	}
}

func TestRunProtocolUnknownToolKeepsFailureOnStdout(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	err := runProtocolWithService(context.Background(), strings.NewReader(`{"tool":"unknown","args":{},"request_id":"bad-1"}`), &stdout, webfetch.NewService(webfetch.Config{}))
	var protocolErr *protocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	var response webfetch.WireResponse
	if decodeErr := json.Unmarshal(stdout.Bytes(), &response); decodeErr != nil {
		t.Fatalf("decode response: %v", decodeErr)
	}
	if response.OK || response.RequestID != "bad-1" || response.ErrorCode != webfetch.ErrorCodeInvalidArgument {
		t.Fatalf("response = %+v", response)
	}
}

func TestRunSchemaPrintsValidatedFunction(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"--schema"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("run schema: %v", err)
	}
	if !strings.Contains(stdout.String(), `"name": "web_search"`) {
		t.Fatalf("schema = %q", stdout.String())
	}
}
