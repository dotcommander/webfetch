package webfetch

import (
	"context"
	"errors"
	"testing"
)

func TestNormalizeRenderOptionsDefaults(t *testing.T) {
	t.Parallel()

	got, err := normalizeRenderOptions(FetchRequest{}, ReaderModeJina)
	if err != nil {
		t.Fatalf("normalizeRenderOptions: %v", err)
	}
	if got.Mode != RenderModeNever || got.Wait != RenderWaitLoad {
		t.Fatalf("render options = %+v, want never/load", got)
	}
}

func TestNormalizeRenderOptionsValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		req    FetchRequest
		reader string
	}{
		{name: "unknown render", req: FetchRequest{Render: "sometimes"}, reader: ReaderModeDefuddle},
		{name: "unknown wait", req: FetchRequest{Render: RenderModeAlways, RenderWait: "sleep"}, reader: ReaderModeDefuddle},
		{name: "wait without render", req: FetchRequest{RenderWait: RenderWaitNetworkIdle}, reader: ReaderModeDefuddle},
		{name: "raw render", req: FetchRequest{Raw: true, Render: RenderModeAlways}, reader: ReaderModeDefuddle},
		{name: "jina render", req: FetchRequest{Render: RenderModeAuto}, reader: ReaderModeJina},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := normalizeRenderOptions(tt.req, tt.reader)
			var coded *CodedError
			if !errors.As(err, &coded) || coded.Code != ErrorCodeInvalidArgument || coded.Suggestion == "" {
				t.Fatalf("error = %v, coded = %+v", err, coded)
			}
		})
	}
}

func TestNormalizeRenderOptionsAcceptsExplicitModes(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{RenderModeAuto, RenderModeAlways} {
		got, err := normalizeRenderOptions(FetchRequest{Render: mode, RenderWait: RenderWaitNetworkIdle}, ReaderModeDefuddle)
		if err != nil {
			t.Fatalf("normalizeRenderOptions(%q): %v", mode, err)
		}
		if got.Mode != mode || got.Wait != RenderWaitNetworkIdle {
			t.Fatalf("render options = %+v", got)
		}
	}
}

func TestServiceFetchRejectsRawRenderBeforeNetwork(t *testing.T) {
	t.Parallel()

	service := NewService(Config{AllowPrivateNetworks: true})
	_, err := service.Fetch(context.Background(), FetchRequest{
		URL:    "http://127.0.0.1:1/unreachable",
		Raw:    true,
		Render: RenderModeAlways,
	})
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != ErrorCodeInvalidArgument {
		t.Fatalf("Fetch error = %v, coded = %+v", err, coded)
	}
}

func TestServiceFetchReturnsStableBrowserUnavailableError(t *testing.T) {
	t.Parallel()

	service := NewService(Config{
		AllowPrivateNetworks: true,
		ChromePath:           "/definitely/missing/webfetch-chrome",
	})
	_, err := service.Fetch(context.Background(), FetchRequest{
		URL:    "http://127.0.0.1:1/unreachable",
		Reader: ReaderModeDefuddle,
		Render: RenderModeAlways,
	})
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != ErrorCodeBrowserUnavailable || coded.Suggestion == "" {
		t.Fatalf("Fetch error = %v, coded = %+v", err, coded)
	}
}
