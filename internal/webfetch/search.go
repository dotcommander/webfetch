package webfetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type searchProvider interface {
	name() string
	search(context.Context, SearchRequest) ([]SearchResult, error)
}

type braveProvider struct {
	client   *Client
	endpoint string
	apiKey   string
}

func (p braveProvider) name() string { return "brave" }

func (p braveProvider) search(ctx context.Context, req SearchRequest) ([]SearchResult, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, newCodedError(errors.New("search query is required"), ErrorCodeInvalidArgument, "provide a non-empty search query")
	}
	if strings.TrimSpace(p.apiKey) == "" {
		return nil, newCodedError(errors.New("BRAVE_API_KEY is required for search"), ErrorCodeMissingAPIKey, "set BRAVE_API_KEY before using the search command")
	}
	if err := rejectBraveOptions(req); err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit == 0 {
		limit = DefaultSearchLimit
	}
	if limit < 1 || limit > MaxSearchLimit {
		return nil, newCodedError(fmt.Errorf("search limit must be between 1 and %d", MaxSearchLimit), ErrorCodeInvalidArgument, fmt.Sprintf("choose a limit from 1 to %d", MaxSearchLimit))
	}

	endpoint, err := url.Parse(p.endpoint)
	if err != nil {
		return nil, newCodedError(fmt.Errorf("parse search endpoint: %w", err), ErrorCodeInvalidArgument, "configure a valid search endpoint")
	}
	params := endpoint.Query()
	params.Set("q", braveSearchQuery(query, req.IncludeDomains))
	params.Set("count", strconv.Itoa(limit))
	endpoint.RawQuery = params.Encode()

	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	headers.Set("X-Subscription-Token", strings.TrimSpace(p.apiKey))
	resp, err := p.client.Get(ctx, endpoint.String(), headers)
	if err != nil {
		return nil, fmt.Errorf("search request: %w", err)
	}

	var payload braveSearchResponse
	if err := decodeJSON(resp.Body, &payload, "search"); err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0, len(payload.Web.Results))
	for _, result := range payload.Web.Results {
		results = append(results, SearchResult{
			Title:       result.Title,
			URL:         result.URL,
			Description: result.Description,
			Highlights:  braveHighlights(req.IncludeHighlights, result.Description),
		})
	}
	return results, nil
}

func rejectBraveOptions(req SearchRequest) error {
	switch {
	case strings.TrimSpace(req.Category) != "":
		return unsupportedSearchOptionError("category")
	case strings.TrimSpace(req.StartPublishedDate) != "":
		return unsupportedSearchOptionError("start_published_date")
	default:
		return nil
	}
}

func braveSearchQuery(query string, domains []string) string {
	if len(domains) == 0 {
		return query
	}
	scopes := make([]string, 0, len(domains))
	for _, domain := range domains {
		scopes = append(scopes, "site:"+domain)
	}
	if len(scopes) == 1 {
		return scopes[0] + " " + query
	}
	return "(" + strings.Join(scopes, " OR ") + ") " + query
}

func braveHighlights(include bool, description string) []string {
	if !include || strings.TrimSpace(description) == "" {
		return nil
	}
	return []string{description}
}

type braveSearchResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
		} `json:"results"`
	} `json:"web"`
}

func (s *Service) Search(ctx context.Context, req SearchRequest) (SearchResponse, error) {
	if s == nil {
		return SearchResponse{}, newCodedError(errors.New("webfetch service is nil"), ErrorCodeInvalidArgument, "initialize the webfetch service before calling Search")
	}
	if s.searchConfig != nil {
		return SearchResponse{}, s.searchConfig
	}
	if s.search == nil {
		return SearchResponse{}, newCodedError(errors.New("search provider is not configured"), ErrorCodeInvalidArgument, "configure a search provider before calling Search")
	}
	validated, err := validateSearchRequest(req, DefaultSearchLimit)
	if err != nil {
		return SearchResponse{}, err
	}
	results, err := s.search.search(ctx, validated)
	if err != nil {
		return SearchResponse{}, err
	}
	return SearchResponse{
		Query:    validated.Query,
		Provider: s.search.name(),
		Results:  normalizeSearchResults(results, validated.Limit),
	}, nil
}

func validateSearchRequest(req SearchRequest, defaultLimit int) (SearchRequest, error) {
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		return SearchRequest{}, newCodedError(errors.New("search query is required"), ErrorCodeInvalidArgument, "provide a non-empty search query")
	}
	if len(req.Query) > 4096 {
		return SearchRequest{}, newCodedError(errors.New("search query must be at most 4096 characters"), ErrorCodeInvalidArgument, "shorten the query before retrying")
	}
	if req.Limit == 0 {
		req.Limit = defaultLimit
	}
	if req.Limit < 1 || req.Limit > MaxSearchLimit {
		return SearchRequest{}, newCodedError(fmt.Errorf("search limit must be between 1 and %d", MaxSearchLimit), ErrorCodeInvalidArgument, fmt.Sprintf("choose a limit from 1 to %d", MaxSearchLimit))
	}
	req.Category = strings.TrimSpace(req.Category)
	if len(req.Category) > 100 {
		return SearchRequest{}, newCodedError(errors.New("search category must be at most 100 characters"), ErrorCodeInvalidArgument, "shorten the category before retrying")
	}
	req.IncludeDomains = append([]string(nil), req.IncludeDomains...)
	for i := range req.IncludeDomains {
		req.IncludeDomains[i] = strings.TrimSpace(req.IncludeDomains[i])
		if req.IncludeDomains[i] == "" || strings.ContainsAny(req.IncludeDomains[i], " \t\r\n/") {
			return SearchRequest{}, newCodedError(fmt.Errorf("invalid search domain at index %d", i), ErrorCodeInvalidArgument, "use hostnames such as example.com without a scheme or path")
		}
	}
	if len(req.IncludeDomains) > 50 {
		return SearchRequest{}, newCodedError(errors.New("search domain filters may contain at most 50 domains"), ErrorCodeInvalidArgument, "reduce the domain filter list before retrying")
	}
	if req.StartPublishedDate != "" {
		normalized, err := normalizeSearchDate(req.StartPublishedDate)
		if err != nil {
			return SearchRequest{}, err
		}
		req.StartPublishedDate = normalized
	}
	if req.IncludeHighlights && req.HighlightSentences == 0 {
		req.HighlightSentences = 3
	}
	if req.HighlightSentences < 0 || req.HighlightSentences > 10 || (req.IncludeHighlights && req.HighlightSentences < 1) {
		return SearchRequest{}, newCodedError(errors.New("highlight sentences must be between 1 and 10"), ErrorCodeInvalidArgument, "choose a highlight sentence count from 1 through 10")
	}
	return req, nil
}

func normalizeSearchResults(results []SearchResult, limit int) []SearchResult {
	seen := make(map[string]struct{}, len(results))
	normalized := make([]SearchResult, 0, min(len(results), limit))
	for _, result := range results {
		result.Title = strings.TrimSpace(result.Title)
		result.URL = strings.TrimSpace(result.URL)
		result.Description = strings.TrimSpace(result.Description)
		result.PublishedAt = strings.TrimSpace(result.PublishedAt)
		result.Highlights = cleanSearchStrings(result.Highlights)
		if result.URL == "" {
			continue
		}
		key := canonicalSearchURL(result.URL)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, result)
		if len(normalized) == limit {
			break
		}
	}
	return normalized
}

func cleanSearchStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func canonicalSearchURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	return parsed.String()
}

func unsupportedSearchProviderError(provider string) error {
	return newCodedError(fmt.Errorf("unsupported search provider %q", provider), ErrorCodeInvalidArgument, "choose search provider brave or exa")
}

func unsupportedSearchOptionError(option string) error {
	return newCodedError(fmt.Errorf("search provider brave does not support %s", option), ErrorCodeUnsupportedSearch, "select the exa search provider for this option")
}
