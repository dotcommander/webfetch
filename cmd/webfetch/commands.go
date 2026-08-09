package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dotcommander/webfetch/internal/webfetch"
)

type fetchCmd struct {
	Raw        bool   `name:"raw" help:"Fetch the URL directly without the reader provider."`
	JSON       bool   `name:"json" help:"Emit a JSON response envelope."`
	Reader     string `name:"reader" default:"jina" enum:"jina,defuddle,auto" help:"Reader to use for HTML pages."`
	Render     string `name:"render" default:"never" enum:"never,auto,always" help:"Browser rendering policy for Defuddle extraction."`
	RenderWait string `name:"render-wait" help:"Browser wait strategy when rendering: load or networkidle; omitted uses load."`
	MaxBytes   int    `name:"max-bytes" default:"0" help:"Limit rendered content bytes; zero means unlimited."`
	MaxLines   int    `name:"max-lines" default:"0" help:"Limit rendered content lines; zero means unlimited."`
	CacheTTL   string `name:"cache-ttl" default:"0" help:"Cache successful reader results for this duration; zero disables caching."`
	CacheDir   string `name:"cache-dir" help:"Directory for cached reader results; used only when cache-ttl is positive."`
	URL        string `arg:"" required:"" help:"URL to fetch."`
}

type searchCmd struct {
	JSON               bool     `name:"json" help:"Emit a JSON response envelope."`
	Provider           string   `name:"provider" help:"Search provider, brave or exa; empty uses configured default."`
	Limit              int      `name:"limit" default:"5" help:"Maximum number of results."`
	Category           string   `name:"category" help:"Provider search category, such as news."`
	IncludeDomains     []string `name:"include-domain" help:"Restrict results to a hostname; may be repeated."`
	StartPublishedDate string   `name:"start-published-date" help:"Lower publication date in RFC3339 or YYYY-MM-DD form."`
	IncludeHighlights  bool     `name:"include-highlights" help:"Request provider-generated highlights."`
	HighlightSentences int      `name:"highlight-sentences" help:"Number of highlight sentences."`
	Query              string   `arg:"" required:"" help:"Search query."`
}

type commandDeps struct {
	ctx     context.Context
	service *webfetch.Service
	stdout  io.Writer
}

type parserExit int

type cliError struct {
	Err  error
	JSON bool
}

func (e *cliError) Error() string {
	return e.Err.Error()
}

func (e *cliError) Unwrap() error {
	return e.Err
}

func (cmd *fetchCmd) Run(deps *commandDeps) error {
	if deps == nil || deps.service == nil {
		return &cliError{Err: errors.New("webfetch service is unavailable"), JSON: cmd.JSON}
	}
	limits, err := newOutputLimits(cmd.MaxBytes, cmd.MaxLines)
	if err != nil {
		return &cliError{
			Err:  webfetch.NewCodedError(err, webfetch.ErrorCodeInvalidArgument, "use non-negative values for max-bytes and max-lines"),
			JSON: cmd.JSON,
		}
	}
	doc, err := deps.service.Fetch(deps.ctx, cmd.fetchRequest())
	if err != nil {
		return &cliError{Err: err, JSON: cmd.JSON}
	}
	if err := renderFetch(deps.stdout, doc, cmd.JSON, limits); err != nil {
		return &cliError{Err: err, JSON: cmd.JSON}
	}
	return nil
}

func (cmd fetchCmd) fetchRequest() webfetch.FetchRequest {
	return webfetch.FetchRequest{
		URL:        cmd.URL,
		Raw:        cmd.Raw,
		Reader:     cmd.Reader,
		Render:     cmd.Render,
		RenderWait: cmd.RenderWait,
	}
}

func parseCacheTTL(value string) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "0" || trimmed == "0s" {
		return 0, nil
	}
	ttl, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, fmt.Errorf("invalid cache TTL %q: use a Go duration such as 24h", value)
	}
	if ttl < 0 {
		return 0, fmt.Errorf("invalid cache TTL %q: duration must not be negative", value)
	}
	return ttl, nil
}

func (cmd *searchCmd) Run(deps *commandDeps) error {
	if deps == nil || deps.service == nil {
		return &cliError{Err: errors.New("webfetch service is unavailable"), JSON: cmd.JSON}
	}
	result, err := deps.service.Search(deps.ctx, webfetch.SearchRequest{
		Query:              cmd.Query,
		Limit:              cmd.Limit,
		Category:           cmd.Category,
		IncludeDomains:     cmd.IncludeDomains,
		StartPublishedDate: cmd.StartPublishedDate,
		IncludeHighlights:  cmd.IncludeHighlights,
		HighlightSentences: cmd.HighlightSentences,
	})
	if err != nil {
		return &cliError{Err: err, JSON: cmd.JSON}
	}
	if err := renderSearch(deps.stdout, result, cmd.JSON); err != nil {
		return &cliError{Err: err, JSON: cmd.JSON}
	}
	return nil
}
