package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/dotcommander/webfetch/internal/webfetch"
	"github.com/voocel/mcp-sdk-go/transport/stdio"
)

func TestRunMCPExplorerHTTPListInspectAndCall(t *testing.T) {
	t.Parallel()

	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Example\n\n## Second\n\nBody\n"))
	}))
	t.Cleanup(reader.Close)
	service := webfetch.NewService(webfetch.Config{
		AllowPrivateNetworks: true,
		JinaAPIKey:           "explorer-test",
		ReaderEndpoint:       reader.URL,
	})
	server := newMCPHTTPHandler(service, "test", mcpToolModeFull, "", nil)
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	var listOut bytes.Buffer
	if err := runMCPExplorer(context.Background(), []string{"list", httpServer.URL}, &listOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed map[string]any
	if err := json.Unmarshal(listOut.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listed["tools"] == nil || listed["server"] == nil {
		t.Fatalf("list output = %#v", listed)
	}

	var inspectOut bytes.Buffer
	if err := runMCPExplorer(context.Background(), []string{"inspect", httpServer.URL, mcpWebFetchTool}, &inspectOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	var inspected map[string]any
	if err := json.Unmarshal(inspectOut.Bytes(), &inspected); err != nil {
		t.Fatalf("decode inspect: %v", err)
	}
	if inspected["name"] != mcpWebFetchTool {
		t.Fatalf("inspect output = %#v", inspected)
	}

	var callOut bytes.Buffer
	if err := runMCPExplorer(context.Background(), []string{
		"call", httpServer.URL, mcpWebFetchTool,
		"-a", "url", "http://example.com/article",
		"-a", "format", "headings",
		"-a", "max_lines", "1",
	}, &callOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("call: %v", err)
	}
	var called map[string]any
	if err := json.Unmarshal(callOut.Bytes(), &called); err != nil {
		t.Fatalf("decode call: %v", err)
	}
	if called["resultType"] != "complete" {
		t.Fatalf("call output = %#v", called)
	}
	structured, ok := called["structuredContent"].(map[string]any)
	if !ok || structured["format"] != "headings" || structured["content"] != "# Example\n" {
		t.Fatalf("structured call output = %#v", called["structuredContent"])
	}
	if truncated, ok := structured["truncated"].(bool); !ok || !truncated {
		t.Fatalf("structured budget output = %#v", structured)
	}
}

func TestRunMCPExplorerHTTPCompactListAndInspect(t *testing.T) {
	t.Parallel()

	httpServer := httptest.NewServer(newMCPHTTPHandler(
		webfetch.NewService(webfetch.Config{}),
		"test",
		mcpToolModeCompact,
		"",
		nil,
	))
	t.Cleanup(httpServer.Close)

	var listOut bytes.Buffer
	if err := runMCPExplorer(context.Background(), []string{"list", httpServer.URL}, &listOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("compact list: %v", err)
	}
	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(listOut.Bytes(), &listed); err != nil {
		t.Fatalf("decode compact list: %v", err)
	}
	if len(listed.Tools) != 1 || listed.Tools[0].Name != mcpCompactTool {
		t.Fatalf("compact list tools = %#v", listed.Tools)
	}

	var inspectOut bytes.Buffer
	if err := runMCPExplorer(context.Background(), []string{"inspect", httpServer.URL, mcpCompactTool}, &inspectOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("compact inspect: %v", err)
	}
	var inspected map[string]any
	if err := json.Unmarshal(inspectOut.Bytes(), &inspected); err != nil {
		t.Fatalf("decode compact inspect: %v", err)
	}
	if inspected["name"] != mcpCompactTool {
		t.Fatalf("compact inspect = %#v", inspected)
	}
}

func TestRunMCPExplorerHTTPBearerToken(t *testing.T) {
	t.Setenv(mcpBearerTokenEnv, "")

	httpServer := httptest.NewServer(newMCPHTTPHandler(
		webfetch.NewService(webfetch.Config{}),
		"test",
		mcpToolModeFull,
		"secret",
		nil,
	))
	t.Cleanup(httpServer.Close)

	var missing bytes.Buffer
	if err := runMCPExplorer(context.Background(), []string{"list", httpServer.URL}, &missing, &bytes.Buffer{}); err == nil {
		t.Fatal("missing explorer token was accepted")
	}

	var authorized bytes.Buffer
	if err := runMCPExplorer(context.Background(), []string{"list", httpServer.URL, "--token", "secret"}, &authorized, &bytes.Buffer{}); err != nil {
		t.Fatalf("authorized explorer list: %v", err)
	}
	if !strings.Contains(authorized.String(), mcpWebFetchTool) {
		t.Fatalf("authorized explorer output = %q", authorized.String())
	}
}

func TestParseMCPExplorerArgumentShorthand(t *testing.T) {
	t.Parallel()

	options, err := parseMCPExplorerOptions("call", []string{
		"http://127.0.0.1:8787",
		mcpWebFetchTool,
		"--args", `{"url":"https://example.com","max_lines":20}`,
		"-a", "max_lines", "2",
		"--argument", "raw", "false",
		"-a", "include_domains", `["example.com"]`,
	})
	if err != nil {
		t.Fatalf("parse shorthand: %v", err)
	}
	want := map[string]any{
		"url":             "https://example.com",
		"max_lines":       json.Number("2"),
		"raw":             false,
		"include_domains": []any{"example.com"},
	}
	if !reflect.DeepEqual(options.Arguments, want) {
		t.Fatalf("arguments = %#v, want %#v", options.Arguments, want)
	}

	withoutArguments, err := parseMCPExplorerOptions("call", []string{"http://127.0.0.1:8787", mcpWebFetchTool})
	if err != nil {
		t.Fatalf("parse empty call: %v", err)
	}
	if withoutArguments.Arguments == nil {
		t.Fatal("empty call arguments is nil")
	}
}

func TestMCPExplorerHelp(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	if err := runMCPExplorer(context.Background(), []string{"--help"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("help: %v", err)
	}
	for _, expected := range []string{"mcp list", "mcp inspect", "mcp call", "--argument", "--command"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("help missing %q: %s", expected, stdout.String())
		}
	}
}

func TestRunMCPExplorerSubprocessList(t *testing.T) {
	t.Setenv("WEBFETCH_MCP_EXPLORER_HELPER", "1")

	var stdout bytes.Buffer
	if err := runMCPExplorer(context.Background(), []string{
		"list", "--command", os.Args[0], "--arg", "-test.run", "--arg", "TestMCPExplorerHelper",
	}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("subprocess list: %v", err)
	}
	if !strings.Contains(stdout.String(), mcpWebFetchTool) {
		t.Fatalf("subprocess output = %q", stdout.String())
	}

	var inspect bytes.Buffer
	if err := runMCPExplorer(context.Background(), []string{
		"inspect", "--command", os.Args[0], "--arg", "-test.run", "--arg", "TestMCPExplorerHelper", mcpWebFetchTool,
	}, &inspect, &bytes.Buffer{}); err != nil {
		t.Fatalf("subprocess inspect: %v", err)
	}
	if !strings.Contains(inspect.String(), `"name": "web_fetch"`) {
		t.Fatalf("subprocess inspect output = %q", inspect.String())
	}
}

func TestRunMCPExplorerSubprocessCompactList(t *testing.T) {
	t.Setenv("WEBFETCH_MCP_EXPLORER_HELPER", "1")
	t.Setenv("WEBFETCH_MCP_EXPLORER_HELPER_MODE", string(mcpToolModeCompact))

	var stdout bytes.Buffer
	if err := runMCPExplorer(context.Background(), []string{
		"list", "--command", os.Args[0], "--arg", "-test.run", "--arg", "TestMCPExplorerHelper",
	}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("compact subprocess list: %v", err)
	}
	if !strings.Contains(stdout.String(), mcpCompactTool) || strings.Contains(stdout.String(), mcpWebFetchTool) {
		t.Fatalf("compact subprocess output = %q", stdout.String())
	}
}

func TestRunMCPExplorerSubprocessCallReturnsToolError(t *testing.T) {
	t.Setenv("WEBFETCH_MCP_EXPLORER_HELPER", "1")

	var stdout bytes.Buffer
	if err := runMCPExplorer(context.Background(), []string{
		"call", "--command", os.Args[0], "--arg", "-test.run", "--arg", "TestMCPExplorerHelper",
		mcpWebFetchTool, "-a", "url", "http://127.0.0.1:1/blocked",
	}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("subprocess call: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode subprocess call: %v", err)
	}
	if result["isError"] != true {
		t.Fatalf("subprocess tool result = %#v", result)
	}
}

func TestMCPExplorerHelper(t *testing.T) {
	if os.Getenv("WEBFETCH_MCP_EXPLORER_HELPER") != "1" {
		return
	}
	service := webfetch.NewService(webfetch.Config{})
	var err error
	if os.Getenv("WEBFETCH_MCP_EXPLORER_HELPER_MODE") == string(mcpToolModeCompact) {
		err = stdio.Serve(context.Background(), newMCPCompactServer(service, "helper"), nil)
	} else {
		err = stdio.Serve(context.Background(), newMCPServer(service, "helper"), nil)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunMCPExplorerRejectsInvalidArgumentsWithoutLeakingToken(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runMCPExplorer(context.Background(), []string{"inspect", "ftp://example.com", "tool", "--token", "secret-token"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("invalid explorer endpoint accepted")
	}
	if strings.Contains(stdout.String()+stderr.String()+err.Error(), "secret-token") {
		t.Fatal("token leaked in explorer error")
	}
}
