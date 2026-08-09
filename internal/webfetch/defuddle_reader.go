package webfetch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	defuddle "github.com/dotcommander/defuddle"
	"golang.org/x/net/html/charset"
)

const htmlReaderAccept = "text/html, application/xhtml+xml, application/xml;q=0.9, text/plain;q=0.8"

type defuddleHTML struct {
	FinalURL    string
	StatusCode  int
	ContentType string
	HTML        string
	Rendered    bool
	Warnings    []string
}

func (p readerProvider) fetchDefuddle(ctx context.Context, target *url.URL) (Document, error) {
	page, err := p.fetchDefuddleHTML(ctx, target)
	if err != nil {
		return Document{}, err
	}
	return parseDefuddleHTML(ctx, target, page)
}

func (p readerProvider) fetchDefuddleHTML(ctx context.Context, target *url.URL) (defuddleHTML, error) {
	headers := make(http.Header)
	headers.Set("Accept", htmlReaderAccept)
	resp, err := p.client.Get(ctx, target.String(), headers)
	if err != nil {
		return defuddleHTML{}, fmt.Errorf("defuddle fetch: %w", err)
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if !isHTMLContentType(contentType) {
		err := fmt.Errorf("defuddle fetch content type %q: %w", contentType, ErrNotHTML)
		return defuddleHTML{}, newCodedError(err, ErrorCodeNotHTML, "use -raw for non-HTML content or provide an HTML page")
	}

	body, err := decodeHTML(resp.Body, contentType)
	if err != nil {
		return defuddleHTML{}, newCodedError(fmt.Errorf("defuddle decode: %w", err), ErrorCodeExtraction, "retry the page or use the jina reader")
	}
	return defuddleHTML{
		FinalURL:    resp.URL,
		StatusCode:  resp.StatusCode,
		ContentType: contentType,
		HTML:        body,
	}, nil
}

func parseDefuddleHTML(ctx context.Context, target *url.URL, page defuddleHTML) (Document, error) {
	finalURL := strings.TrimSpace(page.FinalURL)
	if finalURL == "" && target != nil {
		finalURL = target.String()
	}
	statusCode := page.StatusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	result, err := defuddle.ParseFromString(ctx, page.HTML, &defuddle.Options{
		URL:      finalURL,
		Markdown: true,
	})
	if err != nil {
		return Document{}, newCodedError(fmt.Errorf("defuddle parse: %w", err), ErrorCodeExtraction, "retry the page or use the jina reader")
	}

	content := result.Content
	outputType := page.ContentType
	if result.ContentMarkdown != nil {
		content = *result.ContentMarkdown
		outputType = "text/markdown"
	}

	extractor := ""
	if result.ExtractorType != nil {
		extractor = *result.ExtractorType
	}

	return Document{
		URL:         target.String(),
		FinalURL:    finalURL,
		StatusCode:  statusCode,
		ContentType: outputType,
		Title:       result.Title,
		Description: result.Description,
		Domain:      result.Domain,
		Favicon:     result.Favicon,
		Image:       result.Image,
		Language:    result.Language,
		Published:   result.Published,
		Author:      result.Author,
		Site:        result.Site,
		WordCount:   result.WordCount,
		Extractor:   extractor,
		Source:      ReaderModeDefuddle,
		Rendered:    page.Rendered,
		Warnings:    append([]string(nil), page.Warnings...),
		Content:     content,
	}, nil
}

func isHTMLContentType(contentType string) bool {
	if contentType == "" {
		return true
	}
	lower := strings.ToLower(contentType)
	return strings.Contains(lower, "html") ||
		strings.Contains(lower, "xml") ||
		strings.HasPrefix(lower, "text/")
}

func decodeHTML(body []byte, contentType string) (string, error) {
	reader, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return string(body), nil
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}
