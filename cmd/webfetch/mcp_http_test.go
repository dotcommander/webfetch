package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/dotcommander/webfetch/internal/webfetch"
	mcpclient "github.com/voocel/mcp-sdk-go/client"
	"github.com/voocel/mcp-sdk-go/protocol"
	"github.com/voocel/mcp-sdk-go/transport/mem"
	"github.com/voocel/mcp-sdk-go/transport/streamhttp"
)

func TestValidateMCPHTTPListenRequiresTokenForNonLoopback(t *testing.T) {
	t.Parallel()

	if err := validateMCPHTTPListen("127.0.0.1:8787", ""); err != nil {
		t.Fatalf("loopback listen: %v", err)
	}
	if err := validateMCPHTTPListen("0.0.0.0:8787", ""); err == nil {
		t.Fatal("non-loopback listen accepted without bearer token")
	}
	if err := validateMCPHTTPListen("0.0.0.0:8787", "secret"); err != nil {
		t.Fatalf("protected non-loopback listen: %v", err)
	}
	if err := validateMCPHTTPListen("not-an-address", "secret"); err == nil {
		t.Fatal("invalid listen address accepted")
	}
}

func TestMCPHTTPConfigFromArgs(t *testing.T) {
	t.Setenv(mcpBearerTokenEnv, "  test-token  ")
	cfg, err := mcpHTTPConfigFromArgs([]string{
		"--compact",
		"--listen", "0.0.0.0:9443",
		"--allow-origin", "https://one.example",
		"--allow-origin", "https://two.example",
	})
	if err != nil {
		t.Fatalf("mcpHTTPConfigFromArgs: %v", err)
	}
	if cfg.Listen != "0.0.0.0:9443" || cfg.BearerToken != "test-token" || cfg.Mode != mcpToolModeCompact {
		t.Fatalf("config = %+v", cfg)
	}
	wantOrigins := []string{"https://one.example", "https://two.example"}
	if !reflect.DeepEqual(cfg.AllowedOrigins, wantOrigins) {
		t.Fatalf("allowed origins = %#v, want %#v", cfg.AllowedOrigins, wantOrigins)
	}
}

func TestMCPHTTPConfigRejectsInvalidArgs(t *testing.T) {
	t.Setenv(mcpBearerTokenEnv, "")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing listen value", args: []string{"--listen"}, want: "--listen requires"},
		{name: "missing origin value", args: []string{"--allow-origin"}, want: "--allow-origin requires"},
		{name: "unknown option", args: []string{"--unknown"}, want: "accepts --compact"},
		{name: "unprotected public listen", args: []string{"--listen", "0.0.0.0:8787"}, want: mcpBearerTokenEnv},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mcpHTTPConfigFromArgs(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("mcpHTTPConfigFromArgs(%q) error = %v, want substring %q", tt.args, err, tt.want)
			}
		})
	}
}

func TestNewMCPHTTPServerBoundsHeadersWithoutBreakingStreams(t *testing.T) {
	t.Parallel()

	server := newMCPHTTPServer(http.NotFoundHandler())
	if server.ReadHeaderTimeout != mcpHTTPReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %s, want %s", server.ReadHeaderTimeout, mcpHTTPReadHeaderTimeout)
	}
	if server.IdleTimeout != mcpHTTPIdleTimeout {
		t.Fatalf("IdleTimeout = %s, want %s", server.IdleTimeout, mcpHTTPIdleTimeout)
	}
	if server.MaxHeaderBytes != mcpHTTPMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d, want %d", server.MaxHeaderBytes, mcpHTTPMaxHeaderBytes)
	}
	if server.ReadTimeout != 0 || server.WriteTimeout != 0 {
		t.Fatalf("streaming timeouts = read %s, write %s; want both unset", server.ReadTimeout, server.WriteTimeout)
	}
}

func TestMCPHTTPHandlerServesCurrentDiscovery(t *testing.T) {
	t.Parallel()

	handler := newMCPHTTPHandler(webfetch.NewService(webfetch.Config{}), "test", mcpToolModeFull, "", nil)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	transport := streamhttp.New(server.URL, nil)
	client := mcpclient.New(transport, &mcpclient.Options{
		Info: &protocol.Implementation{Name: "http-test-client", Version: "test"},
	})
	defer client.Close()

	discovery, err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovery.SupportedVersions) != 1 || discovery.SupportedVersions[0] != protocol.Version {
		t.Fatalf("supported versions = %#v", discovery.SupportedVersions)
	}
}

func TestMCPHTTPHandlerForwardsRenderPolicy(t *testing.T) {
	t.Parallel()

	origin := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("raw request reached origin despite invalid render combination")
	}))
	t.Cleanup(origin.Close)

	handler := newMCPHTTPHandler(webfetch.NewService(webfetch.Config{AllowPrivateNetworks: true}), "test", mcpToolModeFull, "", nil)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcpclient.New(streamhttp.New(server.URL, nil), &mcpclient.Options{
		Info: &protocol.Implementation{Name: "http-render-test-client", Version: "test"},
	})
	defer client.Close()

	result, err := client.CallTool(context.Background(), &protocol.CallToolParams{
		Name: mcpWebFetchTool,
		Arguments: map[string]any{
			"url": origin.URL, "raw": true, "render": "always", "render_wait": "networkidle",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("render validation result = %+v", result)
	}
	text := result.Content[0].(protocol.TextContent).Text
	if !strings.Contains(text, "raw fetches cannot render HTML") {
		t.Fatalf("render validation error = %q", text)
	}
}

func TestMCPHTTPHandlerRequiresAndAcceptsBearerToken(t *testing.T) {
	t.Parallel()

	handler := newMCPHTTPHandler(webfetch.NewService(webfetch.Config{}), "test", mcpToolModeFull, "secret", nil)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	requestBody := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
	request := func(token string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(requestBody))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Mcp-Protocol-Version", protocol.Version)
		req.Header.Set("Mcp-Method", "server/discover")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	missing := request("")
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusUnauthorized || missing.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("missing token status = %d headers = %#v", missing.StatusCode, missing.Header)
	}

	valid := request("secret")
	defer valid.Body.Close()
	if valid.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(valid.Body)
		t.Fatalf("valid token status = %d body = %s", valid.StatusCode, body)
	}
}

func TestMCPHTTPDiscoveryAdvertisesCacheControl(t *testing.T) {
	t.Parallel()

	handler := newMCPHTTPHandler(webfetch.NewService(webfetch.Config{}), "test", mcpToolModeFull, "", nil)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	transport := streamhttp.New(server.URL, nil)
	client := mcpclient.New(transport, &mcpclient.Options{
		Info: &protocol.Implementation{Name: "http-test-client", Version: "test"},
	})
	defer client.Close()

	tools, err := client.ListTools(context.Background(), "")
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if tools.TTLMs != 60000 || tools.CacheScope != protocol.CacheScopePublic {
		t.Fatalf("cache control = %+v", tools.CacheControl)
	}
}

func TestMCPHTTPLoggingIsOptIn(t *testing.T) {
	t.Setenv(mcpLogLevelEnv, "debug")
	var logs bytes.Buffer
	srv := newMCPServerWithLogger(webfetch.NewService(webfetch.Config{}), "test", mcpToolModeFull, &logs)
	client := mcpclient.New(mem.New(srv), &mcpclient.Options{
		Info: &protocol.Implementation{Name: "logging-client", Version: "test"},
	})
	defer client.Close()

	if _, err := client.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !strings.Contains(logs.String(), "server/discover") || !strings.Contains(logs.String(), "elapsed_ms") {
		t.Fatalf("logs = %q", logs.String())
	}
	if strings.Contains(logs.String(), "logging-client") == false {
		t.Fatalf("logs omitted client info: %q", logs.String())
	}
}

func TestMCPLoggingDefaultsToSilent(t *testing.T) {
	t.Setenv(mcpLogLevelEnv, "")
	var logs bytes.Buffer
	srv := newMCPServerWithLogger(webfetch.NewService(webfetch.Config{}), "test", mcpToolModeFull, &logs)
	client := mcpclient.New(mem.New(srv), &mcpclient.Options{
		Info: &protocol.Implementation{Name: "silent-client", Version: "test"},
	})
	defer client.Close()

	if _, err := client.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if logs.Len() != 0 {
		t.Fatalf("default logs = %q", logs.String())
	}
}

func TestMCPCompactServerAdvertisesCacheControl(t *testing.T) {
	t.Parallel()

	client := connectMCPTestPeerWithMode(t, webfetch.NewService(webfetch.Config{}), mcpToolModeCompact)
	defer client.Close()

	tools, err := client.ListTools(context.Background(), "")
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if tools.TTLMs != 60000 || tools.CacheScope != protocol.CacheScopePublic {
		t.Fatalf("compact cache control = %+v", tools.CacheControl)
	}
}

func TestMCPHTTPOutputIsStructuredJSON(t *testing.T) {
	t.Parallel()

	handler := newMCPHTTPHandler(webfetch.NewService(webfetch.Config{}), "test", mcpToolModeFull, "", nil)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	body := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	req, err := http.NewRequest(http.MethodPost, server.URL, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Protocol-Version", protocol.Version)
	req.Header.Set("Mcp-Method", "server/discover")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded["result"] == nil {
		t.Fatalf("response = %#v", decoded)
	}
}
