package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dotcommander/webfetch/internal/webfetch"
	"github.com/voocel/mcp-sdk-go/protocol"
)

func TestMCPFetchSupportsHeadingProjectionAndContinuation(t *testing.T) {
	t.Parallel()

	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# First\n\n## Second\n\nBody\n"))
	}))
	t.Cleanup(reader.Close)

	client := connectMCPTestPeerWithService(t, webfetch.NewService(webfetch.Config{
		AllowPrivateNetworks: true,
		JinaAPIKey:           "projection-test",
		ReaderEndpoint:       reader.URL,
	}))
	defer client.Close()

	result, err := client.CallTool(context.Background(), &protocol.CallToolParams{
		Name: mcpWebFetchTool,
		Arguments: map[string]any{
			"url":       "http://example.com/article",
			"format":    "headings",
			"max_lines": 1,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("projection result = %+v", result)
	}
	structured := decodeMCPStructured(t, result)
	if structured["format"] != "headings" || structured["content"] != "# First\n" {
		t.Fatalf("structured projection = %#v", structured)
	}
	if got, ok := structured["next_start_line"].(float64); !ok || got != 1 {
		t.Fatalf("next_start_line = %#v", structured["next_start_line"])
	}
}

func TestMCPFetchSupportsLinkProjectionAndOffsets(t *testing.T) {
	t.Parallel()

	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("[one](https://example.com/one)\n[two](https://example.com/two)\n[again](https://example.com/one)\n"))
	}))
	t.Cleanup(reader.Close)

	client := connectMCPTestPeerWithService(t, webfetch.NewService(webfetch.Config{
		AllowPrivateNetworks: true,
		JinaAPIKey:           "projection-test",
		ReaderEndpoint:       reader.URL,
	}))
	defer client.Close()

	result, err := client.CallTool(context.Background(), &protocol.CallToolParams{
		Name: mcpWebFetchTool,
		Arguments: map[string]any{
			"url":        "http://example.com/article",
			"format":     "links",
			"start_line": 1,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("link projection result = %+v", result)
	}
	structured := decodeMCPStructured(t, result)
	if structured["format"] != "links" || structured["content"] != "https://example.com/two\n" {
		t.Fatalf("structured links = %#v", structured)
	}
}

func TestMCPFetchRejectsRawProjectionConflict(t *testing.T) {
	t.Parallel()

	client := connectMCPTestPeer(t)
	defer client.Close()

	result, err := client.CallTool(context.Background(), &protocol.CallToolParams{
		Name: mcpWebFetchTool,
		Arguments: map[string]any{
			"url":    "https://example.com",
			"raw":    true,
			"format": "headings",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("conflict result = %+v", result)
	}
	text := result.Content[0].(protocol.TextContent).Text
	if !strings.Contains(text, "format") || !strings.Contains(text, "raw") {
		t.Fatalf("conflict error = %q", text)
	}
}

func TestMCPFetchAcceptsWhitespacePaddedMarkdownFormatWithRaw(t *testing.T) {
	t.Parallel()

	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("raw body"))
	}))
	t.Cleanup(reader.Close)

	client := connectMCPTestPeerWithService(t, webfetch.NewService(webfetch.Config{
		AllowPrivateNetworks: true,
		ReaderEndpoint:       reader.URL,
	}))
	defer client.Close()

	result, err := client.CallTool(context.Background(), &protocol.CallToolParams{
		Name: mcpWebFetchTool,
		Arguments: map[string]any{
			"url":    reader.URL + "/article",
			"raw":    true,
			"format": " Markdown ",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("raw markdown result = %+v", result)
	}
}

func TestMCPFetchResultIncludesRenderMetadata(t *testing.T) {
	t.Parallel()

	result, err := mcpFetchResultFromDocument(webfetch.Document{
		URL:        "https://example.com/app",
		StatusCode: http.StatusOK,
		Rendered:   true,
		Warnings:   []string{"render failed; used static HTML"},
		Content:    "# App\n",
	}, mcpFetchArguments{}, outputLimits{})
	if err != nil {
		t.Fatalf("mcpFetchResultFromDocument: %v", err)
	}
	if !result.Rendered || len(result.Warnings) != 1 || result.Warnings[0] != "render failed; used static HTML" {
		t.Fatalf("fetch result = %+v", result)
	}
}
