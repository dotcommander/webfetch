package webfetch

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type stubSearchProvider struct {
	results []SearchResult
}

func (p stubSearchProvider) name() string { return "stub" }

func (p stubSearchProvider) search(context.Context, SearchRequest) ([]SearchResult, error) {
	return p.results, nil
}

func TestParseSearchRequestDefaultsAndNormalizes(t *testing.T) {
	req, err := ParseSearchRequest(map[string]any{
		"query":                "  Go releases  ",
		"max_results":          float64(4),
		"category":             " news ",
		"include_domains":      []any{" go.dev ", "example.com"},
		"start_published_date": "2026-08-01",
	})
	if err != nil {
		t.Fatalf("ParseSearchRequest: %v", err)
	}
	if req.Query != "Go releases" || req.Limit != 4 || req.Category != "news" {
		t.Fatalf("request = %+v", req)
	}
	if !reflect.DeepEqual(req.IncludeDomains, []string{"go.dev", "example.com"}) {
		t.Fatalf("domains = %#v", req.IncludeDomains)
	}
	if req.StartPublishedDate != "2026-08-01T00:00:00Z" || !req.IncludeHighlights || req.HighlightSentences != 3 {
		t.Fatalf("date/highlights = %+v", req)
	}
}

func TestParseSearchRequestRejectsInvalidArguments(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "missing query", args: map[string]any{}},
		{name: "too many results", args: map[string]any{"query": "go", "max_results": float64(51)}},
		{name: "bad date", args: map[string]any{"query": "go", "start_published_date": "yesterday"}},
		{name: "bad domain", args: map[string]any{"query": "go", "include_domains": []any{"https://example.com"}}},
		{name: "bad highlights", args: map[string]any{"query": "go", "highlight_sentences": float64(11)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseSearchRequest(tt.args); err == nil {
				t.Fatal("ParseSearchRequest succeeded")
			}
		})
	}
}

func TestServiceSearchNormalizesDeduplicatesAndLimits(t *testing.T) {
	service := &Service{search: stubSearchProvider{results: []SearchResult{
		{Title: " First ", URL: "HTTPS://Example.COM/article#top", Description: " one "},
		{Title: "Duplicate", URL: "https://example.com/article#other"},
		{Title: "No URL"},
		{Title: " Second ", URL: "https://example.com/second", Highlights: []string{" one ", " ", "two"}},
	}}}
	result, err := service.Search(context.Background(), SearchRequest{Query: "  go  ", Limit: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.Query != "go" || result.Provider != "stub" || len(result.Results) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.Results[0].Title != "First" || result.Results[0].URL != "HTTPS://Example.COM/article#top" {
		t.Fatalf("first result = %+v", result.Results[0])
	}
	if !reflect.DeepEqual(result.Results[1].Highlights, []string{"one", "two"}) {
		t.Fatalf("highlights = %#v", result.Results[1].Highlights)
	}
}

func TestServiceRejectsUnsupportedProvider(t *testing.T) {
	service := NewService(Config{SearchProvider: "unknown"})
	_, err := service.Search(context.Background(), SearchRequest{Query: "go"})
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != ErrorCodeInvalidArgument {
		t.Fatalf("error = %v, coded = %+v", err, coded)
	}
}

func TestBraveSearchSupportsDomainFiltersAsSiteQueries(t *testing.T) {
	if got := braveSearchQuery("writing benchmark", []string{"github.com"}); got != "site:github.com writing benchmark" {
		t.Fatalf("single-domain query = %q", got)
	}
	if got := braveSearchQuery("writing benchmark", []string{"github.com", "huggingface.co"}); got != "(site:github.com OR site:huggingface.co) writing benchmark" {
		t.Fatalf("multi-domain query = %q", got)
	}
}

func TestBraveSearchStillRejectsUnsupportedNativeFilters(t *testing.T) {
	for _, request := range []SearchRequest{
		{Query: "writing", Category: "news"},
		{Query: "writing", StartPublishedDate: "2026-08-01T00:00:00Z"},
	} {
		if err := rejectBraveOptions(request); err == nil {
			t.Fatalf("rejectBraveOptions(%+v) succeeded", request)
		} else {
			var coded *CodedError
			if !errors.As(err, &coded) || coded.Code != ErrorCodeUnsupportedSearch {
				t.Fatalf("error = %v, coded = %+v", err, coded)
			}
		}
	}
}
