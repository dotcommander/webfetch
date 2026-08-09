package webfetch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
)

func TestRendererChromeFlagsForceProxyEgress(t *testing.T) {
	t.Parallel()
	flags := rendererChromeFlags("http://127.0.0.1:1234")
	wants := map[string]any{
		"disable-popup-blocking":          false,
		"disable-quic":                    true,
		"force-webrtc-ip-handling-policy": "disable_non_proxied_udp",
		"host-resolver-rules":             "MAP * ~NOTFOUND, EXCLUDE 127.0.0.1",
		"no-sandbox":                      false,
		"proxy-bypass-list":               "<-loopback>",
		"proxy-server":                    "http://127.0.0.1:1234",
		"webrtc-ip-handling-policy":       "disable_non_proxied_udp",
		"webrtc-multiple-routes-enabled":  false,
		"webrtc-nonproxied-udp-enabled":   false,
	}
	for name, want := range wants {
		if got := flags[name]; got != want {
			t.Fatalf("flag %q = %#v, want %#v", name, got, want)
		}
	}
}

func TestChromedpRendererReturnsStableUnavailableError(t *testing.T) {
	t.Parallel()
	renderer, err := newChromedpRenderer(rendererConfig{
		ChromePath:           "/definitely/missing/webfetch-chrome",
		AllowPrivateNetworks: true,
	})
	if err != nil {
		t.Fatalf("newChromedpRenderer: %v", err)
	}
	t.Cleanup(func() { _ = renderer.Close(context.Background()) })
	target, _ := url.Parse("http://127.0.0.1:1/")
	_, err = renderer.Render(context.Background(), target, renderOptions{Mode: RenderModeAlways, Wait: RenderWaitLoad})
	assertCodedError(t, err, ErrorCodeBrowserUnavailable)
}

func TestRenderTrackerEnforcesRequestBudget(t *testing.T) {
	t.Parallel()
	tracker := newRenderTracker(1, nil)
	tracker.handleEvent(&network.EventRequestWillBeSent{
		RequestID: network.RequestID("one"),
		FrameID:   cdp.FrameID("main"),
		Type:      network.ResourceTypeDocument,
	})
	tracker.handleEvent(&network.EventRequestWillBeSent{
		RequestID: network.RequestID("two"),
		FrameID:   cdp.FrameID("main"),
		Type:      network.ResourceTypeScript,
	})
	assertCodedError(t, tracker.Err(), ErrorCodeRenderBudget)
}

func TestRenderTrackerNetworkIdleIgnoresLongLivedStreams(t *testing.T) {
	t.Parallel()
	tracker := newRenderTracker(10, nil)
	tracker.handleEvent(&network.EventRequestWillBeSent{
		RequestID: network.RequestID("events"),
		Type:      network.ResourceTypeEventSource,
	})
	tracker.mu.Lock()
	tracker.lastActivity = time.Now().Add(-time.Second)
	tracker.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tracker.waitForIdle(ctx, 10*time.Millisecond); err != nil {
		t.Fatalf("waitForIdle: %v", err)
	}
}

func TestRenderSnapshotExpressionOmitsOversizedHTML(t *testing.T) {
	t.Parallel()
	expression := renderSnapshotExpression(123)
	if !strings.Contains(expression, "bytes > 123") || !strings.Contains(expression, "{bytes}") {
		t.Fatalf("snapshot expression does not enforce byte limit: %s", expression)
	}
}

func TestValidateRenderRequestURLRejectsNonHTTPAndUnsafeTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		code string
	}{
		{name: "data", raw: "data:text/html,blocked", code: ErrorCodeInvalidURL},
		{name: "file", raw: "file:///etc/hosts", code: ErrorCodeInvalidURL},
		{name: "websocket", raw: "wss://example.com/socket", code: ErrorCodeInvalidURL},
		{name: "credentials", raw: "https://user:pass@example.com/", code: ErrorCodeInvalidURL},
		{name: "private", raw: "http://127.0.0.1:80/", code: ErrorCodePrivateNetwork},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertCodedError(t, validateRenderRequestURL(context.Background(), tt.raw, false), tt.code)
		})
	}
}

func TestResponseContentTypePreservesHeaderParameters(t *testing.T) {
	t.Parallel()
	response := &network.Response{
		MimeType: "text/html",
		Headers: network.Headers{
			"content-type": "text/html; charset=utf-8",
		},
	}
	if got := responseContentType(response); got != "text/html; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
}

func TestChromedpRendererControlledPage(t *testing.T) {
	chromePath := strings.TrimSpace(os.Getenv("WEBFETCH_TEST_CHROME_PATH"))
	if chromePath == "" {
		t.Skip("WEBFETCH_TEST_CHROME_PATH is not set")
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body><main id="app"></main><script>document.querySelector('#app').textContent='hydrated';</script></body></html>`)
	}))
	t.Cleanup(origin.Close)
	renderer, err := newChromedpRenderer(rendererConfig{
		Timeout:              10 * time.Second,
		MaxConcurrency:       1,
		MaxRequests:          20,
		MaxNetworkBytes:      1 << 20,
		MaxHTMLBytes:         1 << 20,
		ChromePath:           chromePath,
		AllowPrivateNetworks: true,
	})
	if err != nil {
		t.Fatalf("newChromedpRenderer: %v", err)
	}
	t.Cleanup(func() { _ = renderer.Close(context.Background()) })
	target, _ := url.Parse(origin.URL)
	page, err := renderer.Render(context.Background(), target, renderOptions{Mode: RenderModeAlways, Wait: RenderWaitLoad})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(page.HTML, "hydrated") {
		t.Fatalf("rendered HTML = %q, want hydrated content", page.HTML)
	}
	if page.FinalURL != origin.URL+"/" && page.FinalURL != origin.URL {
		t.Fatalf("final URL = %q, want %q", page.FinalURL, origin.URL)
	}
}

func TestServiceFetchControlledRender(t *testing.T) {
	chromePath := strings.TrimSpace(os.Getenv("WEBFETCH_TEST_CHROME_PATH"))
	if chromePath == "" {
		t.Skip("WEBFETCH_TEST_CHROME_PATH is not set")
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body><main id="app"></main><script>document.querySelector('#app').innerHTML='<article><h1>Rendered article</h1><p>Browser-backed Defuddle content.</p></article>';</script></body></html>`)
	}))
	t.Cleanup(origin.Close)

	service := NewService(Config{
		AllowPrivateNetworks:  true,
		ChromePath:            chromePath,
		RenderTimeout:         10 * time.Second,
		RenderMaxConcurrency:  1,
		RenderMaxRequests:     20,
		RenderMaxNetworkBytes: 1 << 20,
		RenderMaxHTMLBytes:    1 << 20,
	})
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	doc, err := service.Fetch(context.Background(), FetchRequest{
		URL:    origin.URL,
		Reader: ReaderModeDefuddle,
		Render: RenderModeAlways,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !doc.Rendered || !strings.Contains(doc.Content, "Rendered article") {
		t.Fatalf("document = %+v", doc)
	}
}
