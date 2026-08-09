package webfetch

import (
	"context"
	_ "embed"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

//go:embed testdata/article.html
var defuddleArticleFixture string

func TestServiceFetchDefuddle(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "text/html") {
			t.Errorf("Accept = %q, want HTML media type", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(defuddleArticleFixture))
	}))
	t.Cleanup(server.Close)

	service := NewService(Config{AllowPrivateNetworks: true})
	doc, err := service.Fetch(context.Background(), FetchRequest{
		URL:    server.URL + "/article",
		Reader: ReaderModeDefuddle,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if doc.Source != ReaderModeDefuddle || doc.ContentType != "text/markdown" {
		t.Fatalf("document mode/type = %q/%q", doc.Source, doc.ContentType)
	}
	if doc.Title != "Local Extraction Test" {
		t.Fatalf("title = %q", doc.Title)
	}
	if doc.Description != "A fixture for local extraction." || doc.Author != "Ada Lovelace" {
		t.Fatalf("metadata = description=%q author=%q", doc.Description, doc.Author)
	}
	if doc.Language != "en-US" || doc.WordCount < 100 {
		t.Fatalf("language/word count = %q/%d", doc.Language, doc.WordCount)
	}
	if !strings.Contains(doc.Content, "A useful article") {
		t.Fatalf("content does not contain article text: %q", doc.Content)
	}
	if strings.Contains(doc.Content, "Advertisement") || strings.Contains(doc.Content, "Sidebar noise") {
		t.Fatalf("content retained clutter: %q", doc.Content)
	}
}

func TestParseDefuddleHTMLSharesRenderedMetadataMapping(t *testing.T) {
	t.Parallel()

	target, err := url.Parse("https://example.com/requested")
	if err != nil {
		t.Fatal(err)
	}
	page := defuddleHTML{
		FinalURL:    "https://example.com/final",
		StatusCode:  http.StatusCreated,
		ContentType: "text/html; charset=utf-8",
		HTML:        defuddleArticleFixture,
		Rendered:    true,
		Warnings:    []string{"render fallback used"},
	}
	doc, err := parseDefuddleHTML(context.Background(), target, page)
	if err != nil {
		t.Fatalf("parseDefuddleHTML: %v", err)
	}
	if doc.Source != ReaderModeDefuddle || !doc.Rendered || doc.FinalURL != page.FinalURL || doc.StatusCode != page.StatusCode {
		t.Fatalf("document routing metadata = %+v", doc)
	}
	if doc.Title != "Local Extraction Test" || doc.ContentType != "text/markdown" || doc.Author != "Ada Lovelace" {
		t.Fatalf("document extraction metadata = %+v", doc)
	}
	if len(doc.Warnings) != 1 || doc.Warnings[0] != page.Warnings[0] {
		t.Fatalf("warnings = %v", doc.Warnings)
	}
	page.Warnings[0] = "mutated"
	if doc.Warnings[0] != "render fallback used" {
		t.Fatalf("document warnings alias input: %v", doc.Warnings)
	}
}

func TestServiceFetchAutoUsesDefuddleWithoutJina(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/article" {
			t.Errorf("unexpected Jina fallback request: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(defuddleArticleFixture))
	}))
	t.Cleanup(server.Close)

	service := NewService(Config{AllowPrivateNetworks: true, ReaderEndpoint: server.URL})
	doc, err := service.Fetch(context.Background(), FetchRequest{
		URL:    server.URL + "/article",
		Reader: ReaderModeAuto,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Source != ReaderModeDefuddle || doc.Title != "Local Extraction Test" {
		t.Fatalf("document = %+v", doc)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("request count = %d, want one Defuddle request and no Jina fallback", got)
	}
}

func TestServiceFetchAutoFallsBackToJina(t *testing.T) {
	t.Parallel()
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"not":"html"}`))
	}))
	t.Cleanup(targetServer.Close)

	var jinaRequests atomic.Int32
	readerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jinaRequests.Add(1)
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Dynamic Article\n\nJina fallback content\n"))
	}))
	t.Cleanup(readerServer.Close)

	service := NewService(Config{AllowPrivateNetworks: true, ReaderEndpoint: readerServer.URL})
	doc, err := service.Fetch(context.Background(), FetchRequest{
		URL:    targetServer.URL + "/article",
		Reader: ReaderModeAuto,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Source != "reader" || doc.Title != "Dynamic Article" {
		t.Fatalf("document = %+v", doc)
	}
	if got := jinaRequests.Load(); got != 1 {
		t.Fatalf("Jina request count = %d, want one", got)
	}
}

func TestServiceFetchAutoCombinesReaderErrors(t *testing.T) {
	t.Parallel()
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"not":"html"}`))
	}))
	t.Cleanup(targetServer.Close)

	readerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Respond-Error", "reader unavailable")
		_, _ = w.Write([]byte("error: reader unavailable"))
	}))
	t.Cleanup(readerServer.Close)

	service := NewService(Config{AllowPrivateNetworks: true, ReaderEndpoint: readerServer.URL})
	_, err := service.Fetch(context.Background(), FetchRequest{
		URL:    targetServer.URL + "/article",
		Reader: ReaderModeAuto,
	})
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != ErrorCodeReaderFallback || coded.Suggestion == "" {
		t.Fatalf("Fetch error = %v, coded = %+v", err, coded)
	}
	if !strings.Contains(err.Error(), "defuddle") || !strings.Contains(err.Error(), "jina") {
		t.Fatalf("combined error = %v", err)
	}
}

func TestServiceFetchAutoStopsWhenContextCanceled(t *testing.T) {
	t.Parallel()
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("canceled request reached target server")
	}))
	t.Cleanup(targetServer.Close)

	var jinaRequests atomic.Int32
	readerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jinaRequests.Add(1)
	}))
	t.Cleanup(readerServer.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := NewService(Config{AllowPrivateNetworks: true, ReaderEndpoint: readerServer.URL})
	_, err := service.Fetch(ctx, FetchRequest{URL: targetServer.URL, Reader: ReaderModeAuto})
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != ErrorCodeCanceled {
		t.Fatalf("Fetch error = %v, coded = %+v", err, coded)
	}
	if got := jinaRequests.Load(); got != 0 {
		t.Fatalf("Jina request count = %d, want zero", got)
	}
}

func TestServiceFetchDefuddleRejectsNonHTML(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("not HTML"))
	}))
	t.Cleanup(server.Close)

	service := NewService(Config{AllowPrivateNetworks: true})
	_, err := service.Fetch(context.Background(), FetchRequest{
		URL:    server.URL,
		Reader: ReaderModeDefuddle,
	})
	if !errors.Is(err, ErrNotHTML) {
		t.Fatalf("Fetch error = %v, want ErrNotHTML", err)
	}
}

func TestServiceFetchDefuddleHonorsBodyLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(strings.Repeat("x", 128)))
	}))
	t.Cleanup(server.Close)

	service := NewService(Config{
		AllowPrivateNetworks: true,
		MaxBodyBytes:         32,
	})
	_, err := service.Fetch(context.Background(), FetchRequest{
		URL:    server.URL,
		Reader: ReaderModeDefuddle,
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Fetch error = %v, want ErrTooLarge", err)
	}
}

func TestDecodeHTMLConvertsWindows1252(t *testing.T) {
	t.Parallel()

	decoded, err := decodeHTML([]byte{'<', 'p', '>', 'c', 'a', 'f', '\xe9', '<', '/', 'p', '>'}, "text/html; charset=windows-1252")
	if err != nil {
		t.Fatalf("decodeHTML: %v", err)
	}
	if decoded != "<p>café</p>" {
		t.Fatalf("decoded = %q", decoded)
	}
}

func TestServiceFetchRejectsUnknownReader(t *testing.T) {
	t.Parallel()

	service := NewService(Config{AllowPrivateNetworks: true})
	_, err := service.Fetch(context.Background(), FetchRequest{
		URL:    "http://127.0.0.1:8080/article",
		Reader: "unknown",
	})
	if !errors.Is(err, ErrUnsupportedReader) {
		t.Fatalf("Fetch error = %v, want ErrUnsupportedReader", err)
	}
}
