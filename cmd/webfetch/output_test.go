package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dotcommander/webfetch/internal/webfetch"
)

func TestRenderSearchText(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := renderSearch(&output, webfetch.SearchResponse{Query: "empty"}, false); err != nil {
		t.Fatalf("render empty search: %v", err)
	}
	if output.String() != "No results.\n" {
		t.Fatalf("empty output = %q", output.String())
	}

	output.Reset()
	result := webfetch.SearchResponse{
		Query: "go",
		Results: []webfetch.SearchResult{
			{Title: "Go", URL: "https://go.dev", Description: "The Go language."},
			{Title: "Packages", URL: "https://pkg.go.dev"},
		},
	}
	if err := renderSearch(&output, result, false); err != nil {
		t.Fatalf("render search results: %v", err)
	}
	for _, want := range []string{"1. Go", "https://go.dev", "The Go language.", "2. Packages", "https://pkg.go.dev"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("search output %q does not contain %q", output.String(), want)
		}
	}
}
