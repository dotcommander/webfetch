package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dotcommander/webfetch/internal/webfetch"
)

func TestRunNoArgsPrintsUsage(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	if err := run(context.Background(), nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "webfetch search") {
		t.Fatalf("usage = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "reader") {
		t.Fatalf("usage omits reader selection = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "auto") {
		t.Fatalf("usage omits auto reader = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "cache-ttl") {
		t.Fatalf("usage omits cache controls = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "render-wait") || !strings.Contains(stdout.String(), "networkidle") {
		t.Fatalf("usage omits render controls = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "--mcp --compact") || !strings.Contains(stdout.String(), "--mcp-compact") {
		t.Fatalf("usage omits compact MCP mode = %q", stdout.String())
	}
}

func TestFetchCmdBuildsRenderRequest(t *testing.T) {
	t.Parallel()

	got := (fetchCmd{
		URL:        "https://example.com/app",
		Raw:        true,
		Reader:     "defuddle",
		Render:     "always",
		RenderWait: "networkidle",
	}).fetchRequest()
	if got.URL != "https://example.com/app" || !got.Raw || got.Reader != "defuddle" || got.Render != "always" || got.RenderWait != "networkidle" {
		t.Fatalf("fetch request = %+v", got)
	}
}

func TestRenderEnvironmentParsers(t *testing.T) {
	t.Run("duration", func(t *testing.T) {
		t.Setenv("WEBFETCH_RENDER_TIMEOUT", "45s")
		if got := positiveDurationEnv("WEBFETCH_RENDER_TIMEOUT"); got != 45*time.Second {
			t.Fatalf("duration = %s, want 45s", got)
		}
	})
	t.Run("integer", func(t *testing.T) {
		t.Setenv("WEBFETCH_RENDER_MAX_CONCURRENCY", "3")
		if got := positiveIntEnv("WEBFETCH_RENDER_MAX_CONCURRENCY"); got != 3 {
			t.Fatalf("integer = %d, want 3", got)
		}
	})
	t.Run("int64", func(t *testing.T) {
		t.Setenv("WEBFETCH_RENDER_MAX_HTML_BYTES", "4096")
		if got := positiveInt64Env("WEBFETCH_RENDER_MAX_HTML_BYTES"); got != 4096 {
			t.Fatalf("int64 = %d, want 4096", got)
		}
	})
	t.Run("bad duration", func(t *testing.T) {
		t.Setenv("WEBFETCH_RENDER_TIMEOUT", "soon")
		if got := positiveDurationEnv("WEBFETCH_RENDER_TIMEOUT"); got != 0 {
			t.Fatalf("invalid duration parsed as %s", got)
		}
	})
	t.Run("negative integer", func(t *testing.T) {
		t.Setenv("WEBFETCH_RENDER_MAX_REQUESTS", "-1")
		if got := positiveIntEnv("WEBFETCH_RENDER_MAX_REQUESTS"); got != 0 {
			t.Fatalf("negative integer parsed as %d", got)
		}
	})
	t.Run("bad int64", func(t *testing.T) {
		t.Setenv("WEBFETCH_RENDER_MAX_NETWORK_BYTES", "NaN")
		if got := positiveInt64Env("WEBFETCH_RENDER_MAX_NETWORK_BYTES"); got != 0 {
			t.Fatalf("invalid int64 parsed as %d", got)
		}
	})
}

func TestRunFetchForwardsRenderPolicy(t *testing.T) {
	t.Parallel()

	origin := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("raw request reached origin despite invalid render combination")
	}))
	t.Cleanup(origin.Close)

	err := runWithService(
		context.Background(),
		[]string{"--raw", "--render", "always", "--render-wait", "networkidle", origin.URL},
		&bytes.Buffer{},
		&bytes.Buffer{},
		webfetch.NewService(webfetch.Config{AllowPrivateNetworks: true}),
	)
	if err == nil || !strings.Contains(err.Error(), "raw fetches cannot render HTML") {
		t.Fatalf("runWithService error = %v", err)
	}
}

func TestRunRawJSON(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello"))
	}))
	t.Cleanup(server.Close)

	service := webfetch.NewService(webfetch.Config{AllowPrivateNetworks: true})
	var stdout bytes.Buffer
	if err := runWithService(context.Background(), []string{"-raw", "-json", server.URL}, &stdout, &bytes.Buffer{}, service); err != nil {
		t.Fatalf("runWithService: %v", err)
	}
	var got fetchOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if !got.OK || got.Mode != "raw" || got.Content != "hello" {
		t.Fatalf("output = %+v", got)
	}
}

func TestRunDefuddleJSON(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>CLI Article</title><meta name="description" content="CLI description"></head><body><main><article><h1>CLI Article</h1><p>Readable local content.</p></article></main></body></html>`))
	}))
	t.Cleanup(server.Close)

	service := webfetch.NewService(webfetch.Config{AllowPrivateNetworks: true})
	var stdout bytes.Buffer
	if err := runWithService(context.Background(), []string{"--reader", "defuddle", "--json", server.URL}, &stdout, &bytes.Buffer{}, service); err != nil {
		t.Fatalf("runWithService: %v", err)
	}
	var got fetchOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if !got.OK || got.Mode != "defuddle" || got.Title != "CLI Article" || got.Description != "CLI description" {
		t.Fatalf("output = %+v", got)
	}
}

func TestRunJSONProviderErrorProjection(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Respond-Error", "reader unavailable")
		_, _ = w.Write([]byte("error: reader unavailable"))
	}))
	t.Cleanup(server.Close)

	service := webfetch.NewService(webfetch.Config{
		AllowPrivateNetworks: true,
		ReaderEndpoint:       server.URL,
	})
	var stdout bytes.Buffer
	err := runWithService(context.Background(), []string{"--json", "http://example.com"}, &stdout, &bytes.Buffer{}, service)
	if err == nil {
		t.Fatal("expected provider error")
	}
	var cliErr *cliError
	if !errors.As(err, &cliErr) || !cliErr.JSON {
		t.Fatalf("run error = %v, cli error = %+v", err, cliErr)
	}
	stdout.Reset()
	if err := renderJSONError(&stdout, cliErr.Err); err != nil {
		t.Fatalf("renderJSONError: %v", err)
	}
	var output errorOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if output.OK || output.ErrorCode != webfetch.ErrorCodeProviderResponse || output.Suggestion == "" {
		t.Fatalf("output = %+v", output)
	}
}

func TestRunSearchJSON(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":"Go","url":"https://go.dev","description":"Go"}]}}`))
	}))
	t.Cleanup(server.Close)

	service := webfetch.NewService(webfetch.Config{
		AllowPrivateNetworks: true,
		SearchEndpoint:       server.URL,
		BraveAPIKey:          "test-key",
	})
	var stdout bytes.Buffer
	if err := runWithService(context.Background(), []string{"search", "-limit", "2", "-json", "golang"}, &stdout, &bytes.Buffer{}, service); err != nil {
		t.Fatalf("runWithService: %v", err)
	}
	var got searchOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if !got.OK || got.Query != "golang" || len(got.Results) != 1 {
		t.Fatalf("output = %+v", got)
	}
}

func TestRunRejectsCompletion(t *testing.T) {
	t.Parallel()
	if err := run(context.Background(), []string{"completion"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected completion to be rejected")
	}
}

func TestParseCacheTTL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  time.Duration
		err   bool
	}{
		{name: "empty", input: "", want: 0},
		{name: "zero", input: "0", want: 0},
		{name: "duration", input: "24h", want: 24 * time.Hour},
		{name: "malformed", input: "tomorrow", err: true},
		{name: "negative", input: "-1h", err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCacheTTL(tt.input)
			if (err != nil) != tt.err || got != tt.want {
				t.Fatalf("parseCacheTTL(%q) = (%v, %v), want (%v, error=%v)", tt.input, got, err, tt.want, tt.err)
			}
		})
	}
}

func TestRunRejectsInvalidCacheTTL(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	service := webfetch.NewService(webfetch.Config{AllowPrivateNetworks: true})
	err := runWithService(context.Background(), []string{"--json", "--cache-ttl", "tomorrow", "http://example.com"}, &stdout, &bytes.Buffer{}, service)
	var cliErr *cliError
	if !errors.As(err, &cliErr) || !cliErr.JSON {
		t.Fatalf("run error = %v, cli error = %+v", err, cliErr)
	}
	stdout.Reset()
	if err := renderJSONError(&stdout, cliErr.Err); err != nil {
		t.Fatalf("renderJSONError: %v", err)
	}
	var output errorOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output.ErrorCode != webfetch.ErrorCodeInvalidArgument || output.Suggestion == "" {
		t.Fatalf("output = %+v", output)
	}
}

func TestBinaryJSONErrorProtocol(t *testing.T) {
	t.Parallel()

	bin := filepath.Join(t.TempDir(), "webfetch-test-bin")
	build := exec.Command("go", "build", "-o", bin, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build webfetch: %v\n%s", err, output)
	}

	cmd := exec.Command(bin, "-json", "file:///tmp/example")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("expected invalid URL command to fail")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr must be empty for JSON mode, got %q", stderr.String())
	}

	var output errorOutput
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &output); err != nil {
		t.Fatalf("stdout must contain one JSON error envelope: %v\nstdout: %q", err, stdout.String())
	}
	if output.OK || output.Error == "" {
		t.Fatalf("error output = %+v", output)
	}
	if output.ErrorCode != webfetch.ErrorCodeInvalidURL || output.Suggestion == "" {
		t.Fatalf("error metadata = code=%q suggestion=%q", output.ErrorCode, output.Suggestion)
	}
}

func TestNormalizeFlags(t *testing.T) {
	t.Parallel()
	got := normalizeFlags([]string{"-raw", "-json", "-reader=defuddle", "-reader=auto", "-limit=10", "-max-bytes=100", "-max-lines", "10", "-cache-ttl=24h", "-cache-dir", "cache", "url"})
	want := []string{"--raw", "--json", "--reader=defuddle", "--reader=auto", "--limit=10", "--max-bytes=100", "--max-lines", "10", "--cache-ttl=24h", "--cache-dir", "cache", "url"}
	if len(got) != len(want) {
		t.Fatalf("normalized length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalized[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestProtocolRouting(t *testing.T) {
	regular, err := os.CreateTemp(t.TempDir(), "regular-input")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = regular.Close() })

	if !shouldRunProtocol([]string{"--protocol"}, regular) || !shouldRunProtocol([]string{"-protocol"}, regular) {
		t.Fatal("explicit protocol flag was not selected")
	}
	if shouldRunProtocol([]string{"https://example.com"}, regular) {
		t.Fatal("normal CLI arguments selected protocol mode")
	}
	if !shouldRunProtocol(nil, regular) {
		t.Fatal("redirected regular-file stdin did not select protocol mode")
	}

	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pipeReader.Close()
		_ = pipeWriter.Close()
	})
	if !shouldRunProtocol(nil, pipeReader) {
		t.Fatal("piped stdin did not select protocol mode")
	}
}

func TestCLIAndProtocolErrorsUnwrap(t *testing.T) {
	cause := errors.New("sentinel")
	cliErr := &cliError{Err: cause}
	if cliErr.Error() != "sentinel" || !errors.Is(cliErr, cause) {
		t.Fatalf("CLI error = %q, unwrap=%v", cliErr.Error(), errors.Unwrap(cliErr))
	}
	protocolErr := &protocolError{Err: cause}
	if protocolErr.Error() != "sentinel" || !errors.Is(protocolErr, cause) {
		t.Fatalf("protocol error = %q, unwrap=%v", protocolErr.Error(), errors.Unwrap(protocolErr))
	}
}
