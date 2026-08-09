package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dotcommander/webfetch/internal/webfetch"
)

type fetchOutput struct {
	OK          bool     `json:"ok"`
	Mode        string   `json:"mode"`
	URL         string   `json:"url"`
	FinalURL    string   `json:"final_url,omitempty"`
	StatusCode  int      `json:"status_code"`
	ContentType string   `json:"content_type,omitempty"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Domain      string   `json:"domain,omitempty"`
	Favicon     string   `json:"favicon,omitempty"`
	Image       string   `json:"image,omitempty"`
	Language    string   `json:"language,omitempty"`
	Published   string   `json:"published,omitempty"`
	Author      string   `json:"author,omitempty"`
	Site        string   `json:"site,omitempty"`
	WordCount   int      `json:"word_count,omitempty"`
	Extractor   string   `json:"extractor,omitempty"`
	Rendered    bool     `json:"rendered,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
	Content     string   `json:"content"`
	Truncated   bool     `json:"truncated,omitempty"`
	TruncatedBy string   `json:"truncated_by,omitempty"`
	TotalBytes  int      `json:"total_bytes,omitempty"`
	OutputBytes int      `json:"output_bytes,omitempty"`
	TotalLines  int      `json:"total_lines,omitempty"`
	OutputLines int      `json:"output_lines,omitempty"`
	MaxBytes    int      `json:"max_bytes,omitempty"`
	MaxLines    int      `json:"max_lines,omitempty"`
}

type searchOutput struct {
	OK      bool                    `json:"ok"`
	Query   string                  `json:"query"`
	Results []webfetch.SearchResult `json:"results"`
}

type errorOutput struct {
	OK         bool   `json:"ok"`
	Error      string `json:"error"`
	ErrorCode  string `json:"error_code,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

func renderFetch(w io.Writer, doc webfetch.Document, asJSON bool, limits outputLimits) error {
	projection := projectContent(doc.Content, limits)
	if !asJSON {
		if _, err := io.WriteString(w, projection.Content); err != nil {
			return err
		}
		if projection.Truncated {
			if projection.Content != "" && !strings.HasSuffix(projection.Content, "\n") {
				if _, err := io.WriteString(w, "\n"); err != nil {
					return err
				}
			}
			_, err := fmt.Fprintf(w, "%s\n", truncationMarker(projection))
			return err
		}
		return nil
	}
	return json.NewEncoder(w).Encode(fetchOutput{
		OK:          true,
		Mode:        doc.Source,
		URL:         doc.URL,
		FinalURL:    doc.FinalURL,
		StatusCode:  doc.StatusCode,
		ContentType: doc.ContentType,
		Title:       doc.Title,
		Description: doc.Description,
		Domain:      doc.Domain,
		Favicon:     doc.Favicon,
		Image:       doc.Image,
		Language:    doc.Language,
		Published:   doc.Published,
		Author:      doc.Author,
		Site:        doc.Site,
		WordCount:   doc.WordCount,
		Extractor:   doc.Extractor,
		Rendered:    doc.Rendered,
		Warnings:    doc.Warnings,
		Content:     projection.Content,
		Truncated:   projection.Truncated,
		TruncatedBy: projection.TruncatedBy,
		TotalBytes:  projection.TotalBytes,
		OutputBytes: projection.OutputBytes,
		TotalLines:  projection.TotalLines,
		OutputLines: projection.OutputLines,
		MaxBytes:    limits.MaxBytes,
		MaxLines:    limits.MaxLines,
	})
}

func renderSearch(w io.Writer, result webfetch.SearchResponse, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(searchOutput{OK: true, Query: result.Query, Results: result.Results})
	}
	if len(result.Results) == 0 {
		_, err := fmt.Fprintln(w, "No results.")
		return err
	}
	for index, hit := range result.Results {
		if _, err := fmt.Fprintf(w, "%d. %s\n   %s\n", index+1, hit.Title, hit.URL); err != nil {
			return err
		}
		if hit.Description != "" {
			if _, err := fmt.Fprintf(w, "   %s\n", hit.Description); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderJSONError(w io.Writer, err error) error {
	output := errorOutput{OK: false, Error: err.Error()}
	var coded *webfetch.CodedError
	if errors.As(err, &coded) {
		output.ErrorCode = coded.Code
		output.Suggestion = coded.Suggestion
	}
	return json.NewEncoder(w).Encode(output)
}
