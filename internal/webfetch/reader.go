package webfetch

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type readerProvider struct {
	client   *Client
	endpoint string
	apiKey   string
}

const (
	ReaderModeJina     = "jina"
	ReaderModeDefuddle = "defuddle"
	ReaderModeAuto     = "auto"
)

const jinaResponseSuggestion = "retry the Jina reader or use -reader defuddle"

var jinaErrorPrefixes = []string{
	"error:",
	"unable to",
	"javascript is not available",
	"please enable javascript",
	"access denied",
	"page not found",
	"403 forbidden",
	"404 not found",
}

func (p readerProvider) fetch(ctx context.Context, target *url.URL) (Document, error) {
	readerURL := strings.TrimRight(p.endpoint, "/") + "/" + target.String()
	headers := make(http.Header)
	headers.Set("Accept", "text/markdown")
	headers.Set("X-Return-Format", "markdown")
	if strings.TrimSpace(p.apiKey) != "" {
		headers.Set("Authorization", "Bearer "+strings.TrimSpace(p.apiKey))
	}
	resp, err := p.client.Get(ctx, readerURL, headers)
	if err != nil {
		return Document{}, fmt.Errorf("reader fetch: %w", err)
	}
	if err := validateJinaResponse(resp); err != nil {
		return Document{}, fmt.Errorf("reader response: %w", err)
	}
	return Document{
		URL:         target.String(),
		FinalURL:    resp.URL,
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Title:       markdownTitle(string(resp.Body)),
		Source:      "reader",
		Content:     string(resp.Body),
	}, nil
}

func (p readerProvider) fetchAuto(ctx context.Context, target *url.URL) (Document, error) {
	doc, defuddleErr := p.fetchDefuddle(ctx, target)
	if defuddleErr == nil {
		return doc, nil
	}
	if ctx.Err() != nil {
		return Document{}, codeContextError(ctx.Err())
	}

	doc, jinaErr := p.fetch(ctx, target)
	if jinaErr == nil {
		return doc, nil
	}
	if ctx.Err() != nil {
		return Document{}, codeContextError(ctx.Err())
	}
	return Document{}, newCodedError(
		fmt.Errorf("auto reader failed: defuddle: %v; jina: %v", defuddleErr, jinaErr),
		ErrorCodeReaderFallback,
		"use -reader jina, -reader defuddle, or -raw to choose one path",
	)
}

func validateJinaResponse(resp httpResponse) error {
	if message := strings.TrimSpace(resp.Header.Get("X-Respond-Error")); message != "" {
		return newCodedError(
			fmt.Errorf("reader provider error: %s", message),
			ErrorCodeProviderResponse,
			jinaResponseSuggestion,
		)
	}

	trimmed := strings.TrimSpace(string(resp.Body))
	check := strings.ToLower(trimmed)
	if len(check) > 500 {
		check = check[:500]
	}
	for _, prefix := range jinaErrorPrefixes {
		if strings.HasPrefix(check, prefix) {
			return newCodedError(
				fmt.Errorf("reader provider error: %s", truncate(trimmed, 100)),
				ErrorCodeProviderResponse,
				jinaResponseSuggestion,
			)
		}
	}
	return nil
}

func markdownTitle(markdown string) string {
	lines := strings.SplitN(markdown, "\n", 11)
	metadataLines := lines
	if len(metadataLines) > 10 {
		metadataLines = metadataLines[:10]
	}
	for _, line := range metadataLines {
		line = strings.TrimSpace(line)
		if title, ok := strings.CutPrefix(line, "Title:"); ok {
			if title = strings.TrimSpace(title); title != "" {
				return title
			}
		}
	}

	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}
