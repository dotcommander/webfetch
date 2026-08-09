package webfetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

type fakePageRenderer struct {
	page       renderedPage
	err        error
	calls      atomic.Int32
	closed     atomic.Int32
	lastTarget atomic.Value
}

func (f *fakePageRenderer) Render(_ context.Context, target *url.URL, _ renderOptions) (renderedPage, error) {
	f.calls.Add(1)
	f.lastTarget.Store(target.String())
	if f.err != nil {
		return renderedPage{}, f.err
	}
	return f.page, nil
}

func (f *fakePageRenderer) Close(context.Context) error {
	f.closed.Add(1)
	return nil
}

func TestServiceCloseClosesRendererOnce(t *testing.T) {
	t.Parallel()

	renderer := &fakePageRenderer{}
	service := NewService(Config{})
	service.renderer = renderer
	if err := service.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := service.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := renderer.closed.Load(); got != 1 {
		t.Fatalf("renderer close calls = %d, want 1", got)
	}
}

func TestServiceFetchRenderedDefuddleUsesRenderer(t *testing.T) {
	t.Parallel()

	service := NewService(Config{AllowPrivateNetworks: true})
	service.renderer = &fakePageRenderer{page: renderedPage{
		FinalURL:    "https://example.com/rendered",
		StatusCode:  http.StatusOK,
		ContentType: "text/html; charset=utf-8",
		HTML:        defuddleArticleFixture,
	}}

	doc, err := service.Fetch(context.Background(), FetchRequest{
		URL:    "https://example.com/requested",
		Reader: ReaderModeDefuddle,
		Render: RenderModeAlways,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !doc.Rendered || doc.FinalURL != "https://example.com/rendered" || doc.StatusCode != http.StatusOK {
		t.Fatalf("render metadata = %+v", doc)
	}
	if doc.Source != ReaderModeDefuddle || doc.Title != "Local Extraction Test" {
		t.Fatalf("document = %+v", doc)
	}
	if got := service.renderer.(*fakePageRenderer).calls.Load(); got != 1 {
		t.Fatalf("renderer calls = %d, want 1", got)
	}
}

func TestServiceFetchRenderAutoKeepsStaticPagesStatic(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(defuddleArticleFixture))
	}))
	t.Cleanup(server.Close)

	renderer := &fakePageRenderer{page: renderedPage{HTML: defuddleArticleFixture}}
	service := NewService(Config{AllowPrivateNetworks: true})
	service.renderer = renderer
	doc, err := service.Fetch(context.Background(), FetchRequest{
		URL:    server.URL + "/article",
		Reader: ReaderModeDefuddle,
		Render: RenderModeAuto,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Rendered || renderer.calls.Load() != 0 {
		t.Fatalf("static page escalated unexpectedly: rendered=%v calls=%d", doc.Rendered, renderer.calls.Load())
	}
}

func TestServiceFetchRenderAutoAddsWarningWhenShellRenderFails(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><div id="app"><article><h1>Static fallback</h1><p>` +
			`This server response contains enough content for Defuddle to extract a useful fallback document even though the page advertises a JavaScript application shell.</p>` +
			`</article></div><script src="/app.js"></script></body></html>`))
	}))
	t.Cleanup(server.Close)

	renderer := &fakePageRenderer{err: errors.New("Chrome unavailable")}
	service := NewService(Config{AllowPrivateNetworks: true})
	service.renderer = renderer
	doc, err := service.Fetch(context.Background(), FetchRequest{
		URL:    server.URL + "/shell",
		Reader: ReaderModeDefuddle,
		Render: RenderModeAuto,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Rendered || len(doc.Warnings) != 1 || !strings.Contains(doc.Warnings[0], "render fallback") {
		t.Fatalf("fallback metadata = %+v", doc)
	}
	if !strings.Contains(doc.Content, "Static fallback") {
		t.Fatalf("fallback content = %q", doc.Content)
	}
	if renderer.calls.Load() != 1 {
		t.Fatalf("renderer calls = %d, want 1", renderer.calls.Load())
	}
}

func TestServiceFetchAutoAlwaysUsesRenderedDefuddleBeforeJina(t *testing.T) {
	t.Parallel()

	var jinaRequests atomic.Int32
	readerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jinaRequests.Add(1)
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Unexpected Jina fallback\n"))
	}))
	t.Cleanup(readerServer.Close)

	service := NewService(Config{AllowPrivateNetworks: true, ReaderEndpoint: readerServer.URL})
	service.renderer = &fakePageRenderer{page: renderedPage{
		FinalURL:    "https://example.com/rendered",
		StatusCode:  http.StatusOK,
		ContentType: "text/html; charset=utf-8",
		HTML:        defuddleArticleFixture,
	}}
	doc, err := service.Fetch(context.Background(), FetchRequest{
		URL:    "https://example.com/article",
		Reader: ReaderModeAuto,
		Render: RenderModeAlways,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !doc.Rendered || doc.Source != ReaderModeDefuddle || doc.Title != "Local Extraction Test" {
		t.Fatalf("document = %+v", doc)
	}
	if got := jinaRequests.Load(); got != 0 {
		t.Fatalf("Jina request count = %d, want zero after rendered Defuddle success", got)
	}
}

func TestServiceFetchAutoAlwaysFallsBackToJina(t *testing.T) {
	t.Parallel()

	readerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Jina fallback\n\nFallback content\n"))
	}))
	t.Cleanup(readerServer.Close)

	service := NewService(Config{AllowPrivateNetworks: true, ReaderEndpoint: readerServer.URL})
	service.renderer = &fakePageRenderer{err: errors.New("Chrome unavailable")}
	doc, err := service.Fetch(context.Background(), FetchRequest{
		URL:    "https://example.com/article",
		Reader: ReaderModeAuto,
		Render: RenderModeAlways,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Rendered || doc.Source != "reader" || doc.Title != "Jina fallback" {
		t.Fatalf("document = %+v", doc)
	}
}

func TestServiceFetchAutoAutoRendersLikelyShellBeforeJina(t *testing.T) {
	t.Parallel()

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><div id="__next"></div><script src="/_next/app.js"></script></body></html>`))
	}))
	t.Cleanup(targetServer.Close)

	var jinaRequests atomic.Int32
	readerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jinaRequests.Add(1)
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Unexpected Jina fallback\n"))
	}))
	t.Cleanup(readerServer.Close)

	service := NewService(Config{AllowPrivateNetworks: true, ReaderEndpoint: readerServer.URL})
	service.renderer = &fakePageRenderer{page: renderedPage{
		FinalURL:    targetServer.URL + "/rendered",
		StatusCode:  http.StatusOK,
		ContentType: "text/html; charset=utf-8",
		HTML:        defuddleArticleFixture,
	}}
	doc, err := service.Fetch(context.Background(), FetchRequest{
		URL:    targetServer.URL,
		Reader: ReaderModeAuto,
		Render: RenderModeAuto,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !doc.Rendered || doc.Source != ReaderModeDefuddle || doc.Title != "Local Extraction Test" {
		t.Fatalf("document = %+v", doc)
	}
	if got := jinaRequests.Load(); got != 0 {
		t.Fatalf("Jina request count = %d, want zero after rendered Defuddle success", got)
	}
}

func TestServiceFetchAutoAutoFallsBackToStaticShellBeforeJina(t *testing.T) {
	t.Parallel()

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><div id="__next"><article><h1>Static fallback</h1><p>` +
			`This server response contains enough content for Defuddle to extract a useful fallback document even though the page advertises a JavaScript application shell.</p>` +
			`</article></div><script src="/_next/app.js"></script></body></html>`))
	}))
	t.Cleanup(targetServer.Close)

	var jinaRequests atomic.Int32
	readerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jinaRequests.Add(1)
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Unexpected Jina fallback\n"))
	}))
	t.Cleanup(readerServer.Close)

	service := NewService(Config{AllowPrivateNetworks: true, ReaderEndpoint: readerServer.URL})
	service.renderer = &fakePageRenderer{err: errors.New("Chrome unavailable")}
	doc, err := service.Fetch(context.Background(), FetchRequest{
		URL:    targetServer.URL,
		Reader: ReaderModeAuto,
		Render: RenderModeAuto,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Rendered || len(doc.Warnings) != 1 || !strings.Contains(doc.Warnings[0], "render fallback") {
		t.Fatalf("fallback metadata = %+v", doc)
	}
	if !strings.Contains(doc.Content, "Static fallback") {
		t.Fatalf("fallback content = %q", doc.Content)
	}
	if got := jinaRequests.Load(); got != 0 {
		t.Fatalf("Jina request count = %d, want zero after static fallback", got)
	}
}

func TestServiceFetchDefaultNeverDoesNotInitializeRenderer(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(defuddleArticleFixture))
	}))
	t.Cleanup(server.Close)

	service := NewService(Config{AllowPrivateNetworks: true})
	if _, err := service.Fetch(context.Background(), FetchRequest{URL: server.URL, Reader: ReaderModeDefuddle}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if service.renderer != nil {
		t.Fatal("static fetch initialized the browser renderer")
	}
}
