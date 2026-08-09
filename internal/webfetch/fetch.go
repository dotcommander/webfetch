package webfetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

type Service struct {
	client       Client
	reader       readerProvider
	readerMode   string
	search       searchProvider
	searchConfig error
	cache        *urlCache
	renderConfig rendererConfig
	renderMu     sync.Mutex
	renderer     pageRenderer
	renderErr    error
}

func NewService(cfg Config) *Service {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxBodyBytes := cfg.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	renderTimeout := cfg.RenderTimeout
	if renderTimeout <= 0 {
		renderTimeout = DefaultRenderTimeout
	}
	renderMaxConcurrency := cfg.RenderMaxConcurrency
	if renderMaxConcurrency <= 0 {
		renderMaxConcurrency = DefaultRenderMaxConcurrency
	}
	renderMaxRequests := cfg.RenderMaxRequests
	if renderMaxRequests <= 0 {
		renderMaxRequests = DefaultRenderMaxRequests
	}
	renderMaxNetworkBytes := cfg.RenderMaxNetworkBytes
	if renderMaxNetworkBytes <= 0 {
		renderMaxNetworkBytes = DefaultRenderMaxNetworkBytes
	}
	renderMaxHTMLBytes := cfg.RenderMaxHTMLBytes
	if renderMaxHTMLBytes <= 0 {
		renderMaxHTMLBytes = maxBodyBytes
	}
	readerEndpoint := strings.TrimSpace(cfg.ReaderEndpoint)
	if readerEndpoint == "" {
		readerEndpoint = DefaultReaderEndpoint
	}
	searchEndpoint := strings.TrimSpace(cfg.SearchEndpoint)
	if searchEndpoint == "" {
		searchEndpoint = DefaultSearchEndpoint
	}
	exaSearchEndpoint := strings.TrimSpace(cfg.ExaSearchEndpoint)
	if exaSearchEndpoint == "" {
		exaSearchEndpoint = DefaultExaEndpoint
	}
	readerMode := strings.TrimSpace(cfg.ReaderMode)
	if readerMode == "" {
		readerMode = ReaderModeJina
	}
	searchProviderName := strings.ToLower(strings.TrimSpace(cfg.SearchProvider))
	var search searchProvider
	var searchConfig error
	if searchProviderName != "" && searchProviderName != "brave" && searchProviderName != "exa" {
		searchConfig = unsupportedSearchProviderError(searchProviderName)
	}

	client := NewClient(ClientConfig{
		Timeout:              timeout,
		MaxBodyBytes:         maxBodyBytes,
		AllowPrivateNetworks: cfg.AllowPrivateNetworks,
	})
	switch searchProviderName {
	case "", "brave":
		search = braveProvider{client: client, endpoint: searchEndpoint, apiKey: cfg.BraveAPIKey}
	case "exa":
		search = exaProvider{client: client, endpoint: exaSearchEndpoint, apiKey: cfg.ExaAPIKey}
	}
	return &Service{
		client:       *client,
		reader:       readerProvider{client: client, endpoint: readerEndpoint, apiKey: cfg.JinaAPIKey},
		readerMode:   readerMode,
		search:       search,
		searchConfig: searchConfig,
		cache:        newURLCache(cfg.URLCacheTTL, cfg.URLCacheDir, maxBodyBytes),
		renderConfig: rendererConfig{
			Timeout:              renderTimeout,
			MaxConcurrency:       renderMaxConcurrency,
			MaxRequests:          renderMaxRequests,
			MaxNetworkBytes:      renderMaxNetworkBytes,
			MaxHTMLBytes:         renderMaxHTMLBytes,
			ChromePath:           strings.TrimSpace(cfg.ChromePath),
			AllowPrivateNetworks: cfg.AllowPrivateNetworks,
			UserAgent:            DefaultUserAgent,
		},
	}
}

func (s *Service) Fetch(ctx context.Context, req FetchRequest) (Document, error) {
	if s == nil {
		return Document{}, newCodedError(errors.New("webfetch service is nil"), ErrorCodeInvalidArgument, "initialize the webfetch service before calling Fetch")
	}
	target, err := validateTarget(ctx, req.URL, s.client.allowPrivateNetworks)
	if err != nil {
		return Document{}, err
	}
	if req.Raw {
		if _, err := normalizeRenderOptions(req, ReaderModeDefuddle); err != nil {
			return Document{}, err
		}
		return s.fetchRaw(ctx, target)
	}
	readerMode := strings.TrimSpace(req.Reader)
	if readerMode == "" {
		readerMode = s.readerMode
	}
	if readerMode != ReaderModeJina && readerMode != ReaderModeDefuddle && readerMode != ReaderModeAuto {
		return Document{}, unsupportedReaderError(readerMode)
	}
	renderOptions, err := normalizeRenderOptions(req, readerMode)
	if err != nil {
		return Document{}, err
	}
	if s.cache != nil {
		if doc, ok := s.cache.load(target.String(), readerMode, s.reader.endpoint, renderOptions.Mode, renderOptions.Wait); ok {
			return doc, nil
		}
	}

	doc, fetchErr := s.fetchWithRender(ctx, target, readerMode, renderOptions)
	if fetchErr != nil {
		return Document{}, fetchErr
	}
	if s.cache != nil {
		s.cache.store(target.String(), readerMode, s.reader.endpoint, doc, renderOptions.Mode, renderOptions.Wait)
	}
	return doc, nil
}

func (s *Service) fetchWithRender(ctx context.Context, target *url.URL, readerMode string, options renderOptions) (Document, error) {
	if options.Mode == RenderModeNever {
		switch readerMode {
		case ReaderModeJina:
			return s.reader.fetch(ctx, target)
		case ReaderModeDefuddle:
			return s.reader.fetchDefuddle(ctx, target)
		case ReaderModeAuto:
			return s.reader.fetchAuto(ctx, target)
		default:
			return Document{}, unsupportedReaderError(readerMode)
		}
	}

	switch readerMode {
	case ReaderModeDefuddle:
		return s.fetchRenderedDefuddle(ctx, target, options)
	case ReaderModeAuto:
		return s.fetchRenderedAuto(ctx, target, options)
	default:
		return Document{}, unsupportedReaderError(readerMode)
	}
}

func (s *Service) fetchRenderedDefuddle(ctx context.Context, target *url.URL, options renderOptions) (Document, error) {
	if options.Mode == RenderModeAuto {
		staticPage, err := s.reader.fetchDefuddleHTML(ctx, target)
		if err != nil {
			return Document{}, err
		}
		if classifyHTML(staticPage.HTML) != shellClassLikely {
			return parseDefuddleHTML(ctx, target, staticPage)
		}
		page, renderErr := s.render(ctx, target, options)
		if renderErr == nil {
			return parseDefuddleHTML(ctx, target, defuddleHTMLFromRendered(page))
		}
		if ctx.Err() != nil {
			return Document{}, codeContextError(ctx.Err())
		}
		staticPage.Warnings = []string{renderWarning(renderErr)}
		doc, parseErr := parseDefuddleHTML(ctx, target, staticPage)
		if parseErr == nil {
			return doc, nil
		}
		return Document{}, renderErr
	}

	page, err := s.render(ctx, target, options)
	if err != nil {
		return Document{}, err
	}
	return parseDefuddleHTML(ctx, target, defuddleHTMLFromRendered(page))
}

func (s *Service) fetchRenderedAuto(ctx context.Context, target *url.URL, options renderOptions) (Document, error) {
	if options.Mode == RenderModeAlways {
		page, renderErr := s.render(ctx, target, options)
		if renderErr == nil {
			doc, parseErr := parseDefuddleHTML(ctx, target, defuddleHTMLFromRendered(page))
			if parseErr == nil {
				return doc, nil
			}
			renderErr = parseErr
		}
		if ctx.Err() != nil {
			return Document{}, codeContextError(ctx.Err())
		}
		jinaDoc, jinaErr := s.reader.fetch(ctx, target)
		if jinaErr == nil {
			return jinaDoc, nil
		}
		return Document{}, newCodedError(
			fmt.Errorf("auto reader failed: rendered defuddle: %v; jina: %v", renderErr, jinaErr),
			ErrorCodeReaderFallback,
			"use reader defuddle or render never to choose one path",
		)
	}

	staticPage, staticErr := s.reader.fetchDefuddleHTML(ctx, target)
	if staticErr == nil {
		if classifyHTML(staticPage.HTML) != shellClassLikely {
			if doc, parseErr := parseDefuddleHTML(ctx, target, staticPage); parseErr == nil {
				return doc, nil
			}
		} else if page, renderErr := s.render(ctx, target, options); renderErr == nil {
			if doc, parseErr := parseDefuddleHTML(ctx, target, defuddleHTMLFromRendered(page)); parseErr == nil {
				return doc, nil
			}
		} else {
			if ctx.Err() != nil {
				return Document{}, codeContextError(ctx.Err())
			}
			staticPage.Warnings = []string{renderWarning(renderErr)}
			if doc, parseErr := parseDefuddleHTML(ctx, target, staticPage); parseErr == nil {
				return doc, nil
			}
		}
	}
	if ctx.Err() != nil {
		return Document{}, codeContextError(ctx.Err())
	}
	if doc, jinaErr := s.reader.fetch(ctx, target); jinaErr == nil {
		return doc, nil
	} else if staticErr != nil {
		return Document{}, newCodedError(
			fmt.Errorf("auto reader failed: defuddle: %v; jina: %v", staticErr, jinaErr),
			ErrorCodeReaderFallback,
			"use reader jina, reader defuddle, or render always to choose one path",
		)
	} else {
		return Document{}, newCodedError(
			fmt.Errorf("auto reader failed: defuddle extraction failed; jina: %v", jinaErr),
			ErrorCodeReaderFallback,
			"use reader jina, reader defuddle, or render always to choose one path",
		)
	}
}

func (s *Service) render(ctx context.Context, target *url.URL, options renderOptions) (renderedPage, error) {
	renderer, err := s.ensureRenderer()
	if err != nil {
		return renderedPage{}, err
	}
	page, err := renderer.Render(ctx, target, options)
	if err != nil {
		if ctx.Err() != nil {
			return renderedPage{}, codeContextError(ctx.Err())
		}
		var coded *CodedError
		if errors.As(err, &coded) {
			return renderedPage{}, err
		}
		return renderedPage{}, newCodedError(fmt.Errorf("render %s: %w", target, err), ErrorCodeRenderFailed, "retry without rendering or increase the render timeout")
	}
	if strings.TrimSpace(page.HTML) == "" {
		return renderedPage{}, newCodedError(errors.New("render returned empty HTML"), ErrorCodeRenderFailed, "retry the page or use render never")
	}
	return page, nil
}

func (s *Service) ensureRenderer() (pageRenderer, error) {
	s.renderMu.Lock()
	defer s.renderMu.Unlock()
	if s.renderer != nil {
		return s.renderer, nil
	}
	if s.renderErr != nil {
		return nil, s.renderErr
	}
	renderer, err := newChromedpRenderer(s.renderConfig)
	if err != nil {
		var coded *CodedError
		if errors.As(err, &coded) {
			s.renderErr = err
		} else {
			s.renderErr = newCodedError(fmt.Errorf("initialize browser renderer: %w", err), ErrorCodeBrowserUnavailable, "install Chrome or Chromium and configure WEBFETCH_CHROME_PATH")
		}
		return nil, s.renderErr
	}
	s.renderer = renderer
	return renderer, nil
}

func (s *Service) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.renderMu.Lock()
	renderer := s.renderer
	s.renderer = nil
	s.renderMu.Unlock()
	if renderer == nil {
		return nil
	}
	return renderer.Close(ctx)
}

func defuddleHTMLFromRendered(page renderedPage) defuddleHTML {
	contentType := strings.TrimSpace(page.ContentType)
	if contentType == "" {
		contentType = "text/html"
	}
	return defuddleHTML{
		FinalURL:    page.FinalURL,
		StatusCode:  page.StatusCode,
		ContentType: contentType,
		HTML:        page.HTML,
		Rendered:    true,
		Warnings:    append([]string(nil), page.Warnings...),
	}
}

func renderWarning(err error) string {
	if err == nil {
		return ""
	}
	return "render fallback: " + truncate(err.Error(), 240)
}

func unsupportedReaderError(readerMode string) error {
	return newCodedError(fmt.Errorf("%w %q", ErrUnsupportedReader, readerMode), ErrorCodeUnsupportedReader, "choose reader jina, defuddle, or auto")
}

func (s *Service) fetchRaw(ctx context.Context, target *url.URL) (Document, error) {
	resp, err := s.client.Get(ctx, target.String(), nil)
	if err != nil {
		return Document{}, err
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(resp.Body)
	}
	return Document{
		URL:         target.String(),
		FinalURL:    resp.URL,
		StatusCode:  resp.StatusCode,
		ContentType: contentType,
		Source:      "raw",
		Content:     string(resp.Body),
	}, nil
}
