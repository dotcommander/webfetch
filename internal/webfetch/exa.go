package webfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type exaProvider struct {
	client   *Client
	endpoint string
	apiKey   string
}

func (p exaProvider) name() string { return "exa" }

type exaSearchRequest struct {
	Query              string       `json:"query"`
	Type               string       `json:"type"`
	NumResults         int          `json:"num_results"`
	Category           string       `json:"category,omitempty"`
	StartPublishedDate string       `json:"start_published_date,omitempty"`
	IncludeDomains     []string     `json:"include_domains,omitempty"`
	Contents           *exaContents `json:"contents,omitempty"`
}

type exaContents struct {
	Highlights exaHighlightOptions `json:"highlights"`
}

type exaHighlightOptions struct {
	NumSentences int `json:"num_sentences"`
}

type exaSearchResponse struct {
	Results []exaResult `json:"results"`
}

type exaResult struct {
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	Highlights    []string `json:"highlights"`
	PublishedDate string   `json:"publishedDate"`
}

func (p exaProvider) search(ctx context.Context, req SearchRequest) ([]SearchResult, error) {
	if strings.TrimSpace(p.apiKey) == "" {
		return nil, newCodedError(fmt.Errorf("EXA_API_KEY is required for Exa search"), ErrorCodeMissingAPIKey, "set EXA_API_KEY before selecting the exa search provider")
	}
	body := exaSearchRequest{
		Query:              req.Query,
		Type:               "auto",
		NumResults:         req.Limit,
		Category:           req.Category,
		StartPublishedDate: req.StartPublishedDate,
		IncludeDomains:     append([]string(nil), req.IncludeDomains...),
	}
	if req.IncludeHighlights {
		body.Contents = &exaContents{Highlights: exaHighlightOptions{NumSentences: req.HighlightSentences}}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal Exa request: %w", err)
	}

	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	headers.Set("Content-Type", "application/json")
	headers.Set("x-api-key", strings.TrimSpace(p.apiKey))
	resp, err := p.client.Do(ctx, http.MethodPost, p.endpoint, headers, payload)
	if err != nil {
		return nil, fmt.Errorf("Exa search request: %w", err)
	}

	var decoded exaSearchResponse
	if err := decodeJSON(resp.Body, &decoded, "Exa search"); err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0, len(decoded.Results))
	for _, result := range decoded.Results {
		results = append(results, SearchResult{
			Title:       result.Title,
			URL:         result.URL,
			PublishedAt: result.PublishedDate,
			Highlights:  result.Highlights,
		})
	}
	return results, nil
}
