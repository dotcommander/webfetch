package webfetch

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	RenderModeNever  = "never"
	RenderModeAuto   = "auto"
	RenderModeAlways = "always"

	renderPolicyVersion = 1

	RenderWaitLoad        = "load"
	RenderWaitNetworkIdle = "networkidle"
)

type renderOptions struct {
	Mode string
	Wait string
}

type rendererConfig struct {
	Timeout              time.Duration
	MaxConcurrency       int
	MaxRequests          int
	MaxNetworkBytes      int64
	MaxHTMLBytes         int64
	ChromePath           string
	AllowPrivateNetworks bool
	UserAgent            string
}

type renderedPage struct {
	FinalURL    string
	StatusCode  int
	ContentType string
	HTML        string
	Warnings    []string
}

type pageRenderer interface {
	Render(context.Context, *url.URL, renderOptions) (renderedPage, error)
	Close(context.Context) error
}

func normalizeRenderOptions(req FetchRequest, readerMode string) (renderOptions, error) {
	renderMode := strings.ToLower(strings.TrimSpace(req.Render))
	if renderMode == "" {
		renderMode = RenderModeNever
	}
	switch renderMode {
	case RenderModeNever, RenderModeAuto, RenderModeAlways:
	default:
		return renderOptions{}, newCodedError(
			fmt.Errorf("unsupported render mode %q", req.Render),
			ErrorCodeInvalidArgument,
			"choose render never, auto, or always",
		)
	}

	rawWait := strings.TrimSpace(req.RenderWait)
	wait := strings.ToLower(rawWait)
	if wait == "" {
		wait = RenderWaitLoad
	}
	switch wait {
	case RenderWaitLoad, RenderWaitNetworkIdle:
	default:
		return renderOptions{}, newCodedError(
			fmt.Errorf("unsupported render wait %q", req.RenderWait),
			ErrorCodeInvalidArgument,
			"choose render_wait load or networkidle",
		)
	}
	if renderMode == RenderModeNever && rawWait != "" {
		return renderOptions{}, newCodedError(
			fmt.Errorf("render_wait requires render auto or always"),
			ErrorCodeInvalidArgument,
			"remove render_wait or choose render auto or always",
		)
	}
	if req.Raw && renderMode != RenderModeNever {
		return renderOptions{}, newCodedError(
			fmt.Errorf("raw fetches cannot render HTML"),
			ErrorCodeInvalidArgument,
			"remove raw or choose render never",
		)
	}
	if readerMode == ReaderModeJina && renderMode != RenderModeNever {
		return renderOptions{}, newCodedError(
			fmt.Errorf("the jina reader cannot consume rendered HTML"),
			ErrorCodeInvalidArgument,
			"choose reader defuddle or auto, or choose render never",
		)
	}
	return renderOptions{Mode: renderMode, Wait: wait}, nil
}
