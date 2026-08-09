package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dotcommander/webfetch/internal/webfetch"
	mcpclient "github.com/voocel/mcp-sdk-go/client"
	"github.com/voocel/mcp-sdk-go/protocol"
	"github.com/voocel/mcp-sdk-go/transport/mem"
)

func TestMCPServerDiscoversCurrentProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := connectMCPTestPeer(t)
	defer client.Close()

	discovery, err := client.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovery.SupportedVersions) != 1 || discovery.SupportedVersions[0] != protocol.Version {
		t.Fatalf("supported versions = %#v, want [%q]", discovery.SupportedVersions, protocol.Version)
	}
	if discovery.Capabilities.Tools == nil {
		t.Fatal("missing tools capability")
	}
}

func TestMCPServerListsWebTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := connectMCPTestPeer(t)
	defer client.Close()

	result, err := client.ListTools(ctx, "")
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(result.Tools) != 2 {
		t.Fatalf("tool count = %d, want 2", len(result.Tools))
	}
	if result.Tools[0].Name != mcpWebFetchTool || result.Tools[1].Name != mcpWebSearchTool {
		t.Fatalf("tools = %q, %q", result.Tools[0].Name, result.Tools[1].Name)
	}
	if result.Tools[1].InputSchema == nil {
		t.Fatal("web_search input schema is nil")
	}
}

func TestMCPCompactServerListsOneRouterTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := connectMCPTestPeerWithMode(t, webfetch.NewService(webfetch.Config{}), mcpToolModeCompact)
	defer client.Close()

	result, err := client.ListTools(ctx, "")
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(result.Tools) != 1 || result.Tools[0].Name != mcpCompactTool {
		t.Fatalf("tools = %#v", result.Tools)
	}
	if result.Tools[0].OutputSchema == nil {
		t.Fatal("compact output schema is nil")
	}
	var schema map[string]any
	raw, err := json.Marshal(result.Tools[0].InputSchema)
	if err != nil {
		t.Fatalf("marshal compact input schema: %v", err)
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode compact input schema: %v", err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("schema dialect = %#v", schema["$schema"])
	}
	if oneOf, ok := schema["oneOf"].([]any); !ok || len(oneOf) != 2 {
		t.Fatalf("schema oneOf = %#v", schema["oneOf"])
	}
	defs, ok := schema["$defs"].(map[string]any)
	if !ok || defs["fetch_input"] == nil || defs["search_input"] == nil {
		t.Fatalf("schema defs = %#v", schema["$defs"])
	}
}

func TestMCPFetchSchemasExposeRenderContract(t *testing.T) {
	t.Parallel()

	inputProperties := mcpWebFetchInputSchema["properties"].(map[string]any)
	render := inputProperties["render"].(map[string]any)
	if got := render["enum"]; !reflect.DeepEqual(got, []string{"never", "auto", "always"}) || render["default"] != "never" {
		t.Fatalf("render schema = %#v", render)
	}
	renderWait := inputProperties["render_wait"].(map[string]any)
	if got := renderWait["enum"]; !reflect.DeepEqual(got, []string{"load", "networkidle"}) {
		t.Fatalf("render_wait schema = %#v", renderWait)
	}

	outputProperties := mcpWebFetchOutputSchema["properties"].(map[string]any)
	if outputProperties["rendered"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("rendered output schema = %#v", outputProperties["rendered"])
	}
	warnings := outputProperties["warnings"].(map[string]any)
	if warnings["type"] != "array" || warnings["items"].(map[string]any)["type"] != "string" {
		t.Fatalf("warnings output schema = %#v", warnings)
	}

	defs := mcpCompactInputSchema["$defs"].(map[string]any)
	compactFetch := defs["fetch_input"].(map[string]any)
	if compactFetch["properties"].(map[string]any)["render_wait"] == nil {
		t.Fatalf("compact fetch schema = %#v", compactFetch)
	}
}

func TestMCPFetchArgumentsBuildRenderRequest(t *testing.T) {
	t.Parallel()

	got := (mcpFetchArguments{
		URL:        "https://example.com/app",
		Raw:        true,
		Reader:     "defuddle",
		Render:     "auto",
		RenderWait: "networkidle",
	}).fetchRequest()
	if got.URL != "https://example.com/app" || !got.Raw || got.Reader != "defuddle" || got.Render != "auto" || got.RenderWait != "networkidle" {
		t.Fatalf("fetch request = %+v", got)
	}
}

func TestMCPFullAndCompactForwardRenderPolicy(t *testing.T) {
	t.Parallel()

	origin := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("raw request reached origin despite invalid render combination")
	}))
	t.Cleanup(origin.Close)
	service := webfetch.NewService(webfetch.Config{AllowPrivateNetworks: true})

	tests := []struct {
		name      string
		mode      mcpToolMode
		tool      string
		arguments map[string]any
	}{
		{
			name: "full",
			mode: mcpToolModeFull,
			tool: mcpWebFetchTool,
			arguments: map[string]any{
				"url": origin.URL, "raw": true, "render": "always", "render_wait": "networkidle",
			},
		},
		{
			name: "compact",
			mode: mcpToolModeCompact,
			tool: mcpCompactTool,
			arguments: map[string]any{
				"operation": "fetch",
				"input": map[string]any{
					"url": origin.URL, "raw": true, "render": "always", "render_wait": "networkidle",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := connectMCPTestPeerWithMode(t, service, tt.mode)
			defer client.Close()

			result, err := client.CallTool(context.Background(), &protocol.CallToolParams{Name: tt.tool, Arguments: tt.arguments})
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
		})
	}
}

func TestMCPCompactServerRoutesFetch(t *testing.T) {
	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/http://example.com/article") {
			http.Error(w, "unexpected reader path", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Compact Example\n\nBody\n"))
	}))
	t.Cleanup(reader.Close)

	client := connectMCPTestPeerWithMode(t, webfetch.NewService(webfetch.Config{
		AllowPrivateNetworks: true,
		ReaderEndpoint:       reader.URL,
	}), mcpToolModeCompact)
	defer client.Close()

	result, err := client.CallTool(context.Background(), &protocol.CallToolParams{
		Name: mcpCompactTool,
		Arguments: map[string]any{
			"operation": "fetch",
			"input": map[string]any{
				"url": "http://example.com/article",
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("compact fetch result = %+v", result)
	}
	structured := decodeMCPStructured(t, result)
	if structured["operation"] != "fetch" {
		t.Fatalf("operation = %#v", structured["operation"])
	}
	fetchResult, ok := structured["result"].(map[string]any)
	if !ok || fetchResult["title"] != "Compact Example" || fetchResult["source"] != "reader" {
		t.Fatalf("compact fetch result = %#v", structured["result"])
	}
}

func TestMCPCompactServerRoutesSearch(t *testing.T) {
	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Subscription-Token"); got != "compact-test" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":"Compact Result","url":"https://example.com/compact","description":"Compact description"}]}}`))
	}))
	t.Cleanup(search.Close)

	client := connectMCPTestPeerWithMode(t, webfetch.NewService(webfetch.Config{
		AllowPrivateNetworks: true,
		BraveAPIKey:          "compact-test",
		SearchEndpoint:       search.URL,
	}), mcpToolModeCompact)
	defer client.Close()

	result, err := client.CallTool(context.Background(), &protocol.CallToolParams{
		Name: mcpCompactTool,
		Arguments: map[string]any{
			"operation": "search",
			"input": map[string]any{
				"query":       "compact",
				"max_results": 1,
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("compact search result = %+v", result)
	}
	structured := decodeMCPStructured(t, result)
	if structured["operation"] != "search" {
		t.Fatalf("operation = %#v", structured["operation"])
	}
	searchResult, ok := structured["result"].(map[string]any)
	if !ok || searchResult["provider"] != "brave" || searchResult["query"] != "compact" {
		t.Fatalf("compact search result = %#v", structured["result"])
	}
}

func TestMCPCompactServerReturnsRouterErrorsAsResults(t *testing.T) {
	client := connectMCPTestPeerWithMode(t, webfetch.NewService(webfetch.Config{}), mcpToolModeCompact)
	defer client.Close()

	result, err := client.CallTool(context.Background(), &protocol.CallToolParams{
		Name: mcpCompactTool,
		Arguments: map[string]any{
			"operation": "unknown",
			"input":     map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("router error result = %+v", result)
	}
	text, ok := result.Content[0].(protocol.TextContent)
	if !ok || !strings.Contains(text.Text, "operation") {
		t.Fatalf("router error content = %#v", result.Content[0])
	}
}

func TestMCPServerReturnsToolErrorsAsResults(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := connectMCPTestPeer(t)
	defer client.Close()

	result, err := client.CallTool(ctx, &protocol.CallToolParams{
		Name:      mcpWebSearchTool,
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("missing IsError for invalid tool arguments")
	}
	if len(result.Content) != 1 {
		t.Fatalf("content count = %d, want 1", len(result.Content))
	}
	text, ok := result.Content[0].(protocol.TextContent)
	if !ok || !strings.Contains(text.Text, "query") {
		t.Fatalf("error content = %#v", result.Content[0])
	}
}

func TestMCPServerFetchesReaderThroughTool(t *testing.T) {
	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/http://example.com/article") {
			http.Error(w, "unexpected reader path", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Example\n\nBody\n"))
	}))
	t.Cleanup(reader.Close)

	client := connectMCPTestPeerWithService(t, webfetch.NewService(webfetch.Config{
		AllowPrivateNetworks: true,
		ReaderEndpoint:       reader.URL,
		JinaAPIKey:           "jina-test",
	}))
	defer client.Close()

	result, err := client.CallTool(context.Background(), &protocol.CallToolParams{
		Name: mcpWebFetchTool,
		Arguments: map[string]any{
			"url": "http://example.com/article",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError || result.ResultType() != protocol.ResultTypeComplete {
		t.Fatalf("result = %+v", result)
	}
	structured := decodeMCPStructured(t, result)
	if structured["source"] != "reader" || structured["title"] != "Example" || structured["content"] != "# Example\n\nBody\n" {
		t.Fatalf("structured result = %#v", structured)
	}
}

func TestMCPServerFetchesRawBodyThroughTool(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(origin.Close)

	client := connectMCPTestPeerWithService(t, webfetch.NewService(webfetch.Config{
		AllowPrivateNetworks: true,
	}))
	defer client.Close()

	result, err := client.CallTool(context.Background(), &protocol.CallToolParams{
		Name: mcpWebFetchTool,
		Arguments: map[string]any{
			"url": origin.URL,
			"raw": true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("raw result = %+v", result)
	}
	structured := decodeMCPStructured(t, result)
	if structured["source"] != "raw" || structured["status_code"] != float64(http.StatusOK) || structured["content"] != `{"ok":true}` {
		t.Fatalf("structured result = %#v", structured)
	}
	if contentType, _ := structured["content_type"].(string); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("content type = %q", contentType)
	}
}

func TestMCPServerAppliesFetchOutputBudget(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("one\ntwo\nthree\n"))
	}))
	t.Cleanup(origin.Close)

	client := connectMCPTestPeerWithService(t, webfetch.NewService(webfetch.Config{
		AllowPrivateNetworks: true,
	}))
	defer client.Close()

	result, err := client.CallTool(context.Background(), &protocol.CallToolParams{
		Name: mcpWebFetchTool,
		Arguments: map[string]any{
			"url":       origin.URL,
			"raw":       true,
			"max_lines": 2,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("bounded raw result = %+v", result)
	}
	structured := decodeMCPStructured(t, result)
	if structured["content"] != "one\ntwo\n" || structured["truncated"] != true || structured["truncated_by"] != "lines" {
		t.Fatalf("bounded content = %#v", structured)
	}
	if structured["total_lines"] != float64(3) || structured["output_lines"] != float64(2) || structured["max_lines"] != float64(2) {
		t.Fatalf("bounded metadata = %#v", structured)
	}
}

func TestMCPServerSearchesBraveThroughTool(t *testing.T) {
	var gotToken, gotQuery, gotCount string
	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Subscription-Token")
		gotQuery = r.URL.Query().Get("q")
		gotCount = r.URL.Query().Get("count")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":" Result ","url":"https://example.com/article","description":" Description "}]}}`))
	}))
	t.Cleanup(search.Close)

	client := connectMCPTestPeerWithService(t, webfetch.NewService(webfetch.Config{
		AllowPrivateNetworks: true,
		BraveAPIKey:          "brave-test",
		SearchEndpoint:       search.URL,
	}))
	defer client.Close()

	result, err := client.CallTool(context.Background(), &protocol.CallToolParams{
		Name: mcpWebSearchTool,
		Arguments: map[string]any{
			"query":           "go",
			"max_results":     1,
			"include_domains": []any{"example.com"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("search result = %+v", result)
	}
	if gotToken != "brave-test" || gotQuery != "site:example.com go" || gotCount != "1" {
		t.Fatalf("search request token=%q query=%q count=%q", gotToken, gotQuery, gotCount)
	}
	structured := decodeMCPStructured(t, result)
	if structured["provider"] != "brave" || structured["query"] != "go" {
		t.Fatalf("structured result = %#v", structured)
	}
	results, ok := structured["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("results = %#v", structured["results"])
	}
	hit, ok := results[0].(map[string]any)
	if !ok || hit["title"] != "Result" || hit["url"] != "https://example.com/article" || hit["description"] != "Description" {
		t.Fatalf("search hit = %#v", results[0])
	}
	highlights, ok := hit["highlights"].([]any)
	if !ok || len(highlights) != 1 || highlights[0] != "Description" {
		t.Fatalf("highlights = %#v", hit["highlights"])
	}
}

func TestMCPServerPropagatesCancellation(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	t.Cleanup(reader.Close)

	client := connectMCPTestPeerWithService(t, webfetch.NewService(webfetch.Config{
		AllowPrivateNetworks: true,
		ReaderEndpoint:       reader.URL,
	}))
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	callDone := make(chan struct{})
	var result *protocol.CallToolResult
	var err error
	go func() {
		result, err = client.CallTool(ctx, &protocol.CallToolParams{
			Name: mcpWebFetchTool,
			Arguments: map[string]any{
				"url": "http://example.com/slow",
			},
		})
		close(callDone)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("reader request did not start")
	}
	cancel()
	select {
	case <-callDone:
	case <-time.After(2 * time.Second):
		t.Fatal("MCP call did not return after cancellation")
	}
	if err != nil && !strings.Contains(err.Error(), "stream ended without a response") {
		t.Fatalf("CallTool after cancellation: %v", err)
	}
	if err == nil && (result == nil || !result.IsError) {
		t.Fatalf("cancellation result = %+v", result)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("reader request did not observe cancellation")
	}
}

func TestRunMCPRejectsArguments(t *testing.T) {
	if err := runMCP(context.Background(), []string{"unexpected"}); err == nil {
		t.Fatal("runMCP accepted positional arguments")
	}
}

func TestMCPToolModeSelectionPreservesFullDefault(t *testing.T) {
	t.Setenv(mcpToolModeEnv, "")
	mode, err := mcpToolModeFromArgs(nil)
	if err != nil || mode != mcpToolModeFull {
		t.Fatalf("default mode = %q, err = %v", mode, err)
	}
	mode, err = mcpToolModeFromArgs([]string{"--compact"})
	if err != nil || mode != mcpToolModeCompact {
		t.Fatalf("flag mode = %q, err = %v", mode, err)
	}
	t.Setenv(mcpToolModeEnv, "compact")
	mode, err = mcpToolModeFromArgs(nil)
	if err != nil || mode != mcpToolModeCompact {
		t.Fatalf("environment mode = %q, err = %v", mode, err)
	}
	t.Setenv(mcpToolModeEnv, "invalid")
	if _, err := mcpToolModeFromArgs(nil); err == nil {
		t.Fatal("invalid MCP mode was accepted")
	}
}

func connectMCPTestPeer(t *testing.T) *mcpclient.Client {
	t.Helper()

	return connectMCPTestPeerWithService(t, webfetch.NewService(webfetch.Config{}))
}

func connectMCPTestPeerWithService(t *testing.T, service *webfetch.Service) *mcpclient.Client {
	return connectMCPTestPeerWithMode(t, service, mcpToolModeFull)
}

func connectMCPTestPeerWithMode(t *testing.T, service *webfetch.Service, mode mcpToolMode) *mcpclient.Client {
	t.Helper()

	srv := newMCPServerWithMode(service, "test", mode)
	return mcpclient.New(mem.New(srv), &mcpclient.Options{
		Info: &protocol.Implementation{Name: "webfetch-test-client", Version: "test"},
	})
}

func decodeMCPStructured(t *testing.T, result *protocol.CallToolResult) map[string]any {
	t.Helper()
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var structured map[string]any
	if err := json.Unmarshal(raw, &structured); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
	return structured
}

func TestMCPServerUsesCurrentSDK(t *testing.T) {
	if protocol.Version != "2026-07-28" {
		t.Fatalf("SDK protocol version = %q, want 2026-07-28", protocol.Version)
	}
}

func TestPipedInputDetectsMCPInitialize(t *testing.T) {
	t.Parallel()

	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"apfel","version":"test"}}}` + "\n"
	var output bytes.Buffer
	if err := runPipedInput(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("runPipedInput: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode initialize response: %v\n%s", err, output.String())
	}
	result, ok := response["result"].(map[string]any)
	if !ok || result["protocolVersion"] != legacyMCPProtocolVersion {
		t.Fatalf("initialize response = %#v", response)
	}
}

func TestPipedInputPreservesOneShotProtocol(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	err := runPipedInput(context.Background(), strings.NewReader(`{"tool":"unknown","args":{},"request_id":"pipe-1"}`), &output)
	var protocolErr *protocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	var response webfetch.WireResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode protocol response: %v\n%s", err, output.String())
	}
	if response.OK || response.RequestID != "pipe-1" || response.ErrorCode != webfetch.ErrorCodeInvalidArgument {
		t.Fatalf("response = %+v", response)
	}
}

type blockingMCPReadCloser struct {
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func newBlockingMCPReadCloser() *blockingMCPReadCloser {
	return &blockingMCPReadCloser{
		started: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (r *blockingMCPReadCloser) Read([]byte) (int, error) {
	r.startOnce.Do(func() { close(r.started) })
	<-r.closed
	return 0, io.ErrClosedPipe
}

func (r *blockingMCPReadCloser) Close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return nil
}

func TestMCPServerCancellationClosesBlockedInput(t *testing.T) {
	reader := newBlockingMCPReadCloser()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serveMCP(ctx, newMCPServer(webfetch.NewService(webfetch.Config{}), "test"), "test", reader, io.Discard)
	}()

	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("MCP input rewriter did not start reading")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("serveMCP error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("MCP server did not stop after cancellation")
	}
	select {
	case <-reader.closed:
	default:
		t.Fatal("MCP server left its blocked close-capable input open")
	}
}

const mcpStdioProcessEnv = "WEBFETCH_TEST_MCP_STDIO_PROCESS"

func TestMCPStdioStopsOnInterrupt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt is not supported for child processes on Windows")
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestMCPStdioProcess$")
	cmd.Env = append(os.Environ(), mcpStdioProcessEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("MCP child stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("MCP child stdout: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start MCP child: %v", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	finished := false
	defer func() {
		_ = stdin.Close()
		if !finished {
			_ = cmd.Process.Kill()
			<-wait
		}
	}()

	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"shutdown-test","version":"test"}}}` + "\n"
	if _, err := io.WriteString(stdin, initialize); err != nil {
		t.Fatalf("write MCP initialize: %v", err)
	}
	ready := make(chan error, 1)
	go func() {
		_, err := bufio.NewReader(stdout).ReadBytes('\n')
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("read MCP initialize response: %v; stderr: %s", err, stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MCP child did not become ready")
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt MCP child: %v", err)
	}
	select {
	case <-wait:
		finished = true
	case <-time.After(2 * time.Second):
		t.Fatal("MCP child did not stop after interrupt while stdin remained open")
	}
}

func TestMCPStdioProcess(t *testing.T) {
	if os.Getenv(mcpStdioProcessEnv) != "1" {
		return
	}
	os.Args = []string{"webfetch", "--mcp"}
	main()
}

func TestMCPServerAcceptsLegacyJcodeHandshake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/http://example.com/article") {
			http.Error(w, "unexpected reader path", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Jcode Example\n\nBody\n"))
	}))
	t.Cleanup(reader.Close)

	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- serveMCP(ctx, newMCPServer(webfetch.NewService(webfetch.Config{
			AllowPrivateNetworks: true,
			JinaAPIKey:           "jcode-test",
			ReaderEndpoint:       reader.URL,
		}), "test"), "test", inputReader, outputWriter)
	}()

	output := bufio.NewReader(outputReader)
	send := func(message map[string]any) map[string]any {
		t.Helper()
		raw, err := json.Marshal(message)
		if err != nil {
			t.Fatalf("marshal legacy MCP request: %v", err)
		}
		if _, err := inputWriter.Write(append(raw, '\n')); err != nil {
			t.Fatalf("write legacy MCP request: %v", err)
		}
		line, err := output.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read legacy MCP response: %v", err)
		}
		var response map[string]any
		if err := json.Unmarshal(line, &response); err != nil {
			t.Fatalf("decode legacy MCP response: %v", err)
		}
		return response
	}

	initialize := send(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": legacyMCPProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "jcode", "version": "test"},
		},
	})
	initializeResult, ok := initialize["result"].(map[string]any)
	if !ok || initializeResult["protocolVersion"] != legacyMCPProtocolVersion {
		t.Fatalf("legacy initialize response = %#v", initialize)
	}
	if _, err := inputWriter.Write([]byte(`{"jsonrpc":"2.0","id":0,"method":"notifications/initialized"}` + "\n")); err != nil {
		t.Fatalf("write initialized notification: %v", err)
	}

	tools := send(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	})
	toolsResult, ok := tools["result"].(map[string]any)
	if !ok {
		t.Fatalf("legacy tools/list response = %#v", tools)
	}
	toolList, ok := toolsResult["tools"].([]any)
	if !ok || len(toolList) != 2 {
		t.Fatalf("legacy tools/list tools = %#v", toolsResult["tools"])
	}

	fetch := send(map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name": mcpWebFetchTool,
			"arguments": map[string]any{
				"url": "http://example.com/article",
			},
		},
	})
	fetchResult, ok := fetch["result"].(map[string]any)
	if !ok || fetchResult["isError"] == true {
		t.Fatalf("legacy web_fetch response = %#v", fetch)
	}
	structured, ok := fetchResult["structuredContent"].(map[string]any)
	if !ok || structured["content"] != "# Jcode Example\n\nBody\n" {
		t.Fatalf("legacy web_fetch structured content = %#v", fetchResult["structuredContent"])
	}
	renderFetch := send(map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name": mcpWebFetchTool,
			"arguments": map[string]any{
				"url":         "http://example.com/article",
				"raw":         true,
				"render":      "always",
				"render_wait": "networkidle",
			},
		},
	})
	renderFetchResult, ok := renderFetch["result"].(map[string]any)
	renderFetchJSON, _ := json.Marshal(renderFetchResult)
	if !ok || renderFetchResult["isError"] != true || !strings.Contains(string(renderFetchJSON), "raw fetches cannot render HTML") {
		t.Fatalf("legacy rendered web_fetch response = %#v", renderFetch)
	}

	call := send(map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      mcpWebSearchTool,
			"arguments": map[string]any{},
		},
	})
	callResult, ok := call["result"].(map[string]any)
	if !ok || callResult["isError"] != true {
		t.Fatalf("legacy tools/call response = %#v", call)
	}

	if err := inputWriter.Close(); err != nil {
		t.Fatalf("close legacy MCP input: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve legacy MCP: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("legacy MCP server did not exit after input EOF")
	}
}

func TestMCPToolResultsPreserveEncodingErrors(t *testing.T) {
	_, err := mcpJSONResult(func() {})
	if err == nil || !strings.Contains(err.Error(), "encode MCP tool result") {
		t.Fatalf("mcpJSONResult error = %v", err)
	}
}
