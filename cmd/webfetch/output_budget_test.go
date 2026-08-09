package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dotcommander/webfetch/internal/webfetch"
)

func TestProjectContentUnlimited(t *testing.T) {
	t.Parallel()

	content := "one\ntwo\n"
	got := projectContent(content, outputLimits{})
	if got.Content != content || got.Truncated {
		t.Fatalf("projection = %+v", got)
	}
	if got.TotalLines != 2 || got.OutputLines != 2 {
		t.Fatalf("line counts = total %d output %d", got.TotalLines, got.OutputLines)
	}
}

func TestProjectContentByLines(t *testing.T) {
	t.Parallel()

	got := projectContent("one\ntwo\nthree\n", outputLimits{MaxLines: 2})
	if got.Content != "one\ntwo\n" {
		t.Fatalf("content = %q", got.Content)
	}
	if !got.Truncated || got.TruncatedBy != "lines" {
		t.Fatalf("truncation = %+v", got)
	}
	if got.TotalLines != 3 || got.OutputLines != 2 {
		t.Fatalf("line counts = total %d output %d", got.TotalLines, got.OutputLines)
	}
}

func TestProjectContentByBytesPreservesUTF8(t *testing.T) {
	t.Parallel()

	got := projectContent("😀😀\n", outputLimits{MaxBytes: 5})
	if !got.Truncated || got.TruncatedBy != "bytes" {
		t.Fatalf("truncation = %+v", got)
	}
	if !strings.HasPrefix(got.Content, "😀") || !utf8.ValidString(got.Content) {
		t.Fatalf("content is not a valid UTF-8 prefix: %q", got.Content)
	}
	if got.OutputBytes > 5 {
		t.Fatalf("output bytes = %d, want <= 5", got.OutputBytes)
	}
}

func TestProjectContentUsesByteLimitAfterLineLimit(t *testing.T) {
	t.Parallel()

	got := projectContent("abc\ndef\n", outputLimits{MaxBytes: 3, MaxLines: 1})
	if got.Content != "abc" || got.TruncatedBy != "bytes" {
		t.Fatalf("projection = %+v", got)
	}
	if got.OutputLines != 1 || got.OutputBytes != 3 {
		t.Fatalf("output counts = lines %d bytes %d", got.OutputLines, got.OutputBytes)
	}
}

func TestRenderFetchHumanOutputBudgetMarker(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	err := renderFetch(&stdout, webfetch.Document{Content: "one\ntwo\nthree\n"}, false, outputLimits{MaxLines: 2})
	if err != nil {
		t.Fatalf("renderFetch: %v", err)
	}
	if !strings.Contains(stdout.String(), "one\ntwo\n") || !strings.Contains(stdout.String(), "[truncated:") {
		t.Fatalf("human output = %q", stdout.String())
	}
}

func TestRenderFetchOutputsRenderMetadataOnlyInJSON(t *testing.T) {
	t.Parallel()

	doc := webfetch.Document{
		URL:        "https://example.com/app",
		StatusCode: http.StatusOK,
		Rendered:   true,
		Warnings:   []string{"render failed; used static HTML"},
		Content:    "# App\n",
	}

	var jsonOutput bytes.Buffer
	if err := renderFetch(&jsonOutput, doc, true, outputLimits{}); err != nil {
		t.Fatalf("renderFetch JSON: %v", err)
	}
	var got fetchOutput
	if err := json.Unmarshal(jsonOutput.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	if !got.Rendered || len(got.Warnings) != 1 || got.Warnings[0] != doc.Warnings[0] {
		t.Fatalf("JSON output = %+v", got)
	}

	var humanOutput bytes.Buffer
	if err := renderFetch(&humanOutput, doc, false, outputLimits{}); err != nil {
		t.Fatalf("renderFetch human: %v", err)
	}
	if humanOutput.String() != doc.Content {
		t.Fatalf("human output = %q, want content only %q", humanOutput.String(), doc.Content)
	}
}

func TestRunRawJSONOutputBudget(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("one\ntwo\nthree\n"))
	}))
	t.Cleanup(server.Close)

	service := webfetch.NewService(webfetch.Config{AllowPrivateNetworks: true})
	var stdout bytes.Buffer
	if err := runWithService(context.Background(), []string{"--raw", "--json", "--max-lines", "2", server.URL}, &stdout, &bytes.Buffer{}, service); err != nil {
		t.Fatalf("runWithService: %v", err)
	}

	var got fetchOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if !got.Truncated || got.TruncatedBy != "lines" || got.Content != "one\ntwo\n" {
		t.Fatalf("output = %+v", got)
	}
	if got.TotalLines != 3 || got.OutputLines != 2 || got.MaxLines != 2 {
		t.Fatalf("truncation metadata = %+v", got)
	}
}
