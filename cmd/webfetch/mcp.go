package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/dotcommander/webfetch/internal/webfetch"
	"github.com/voocel/mcp-sdk-go/protocol"
	"github.com/voocel/mcp-sdk-go/server"
)

const (
	mcpWebSearchTool = "web_search"
	mcpWebFetchTool  = "web_fetch"
)

// runMCP starts the MCP 2026-07-28 stdio server. The default full mode preserves
// the web_search and web_fetch tools. --compact or WEBFETCH_MCP_TOOL_MODE=compact
// selects the one-tool context-efficient surface.
func runMCP(ctx context.Context, args []string) error {
	return runMCPWithIO(ctx, args, nil, nil)
}

func runMCPWithIO(ctx context.Context, args []string, reader io.Reader, writer io.Writer) error {
	mode, err := mcpToolModeFromArgs(args)
	if err != nil {
		return err
	}
	service := configuredService(webfetch.Config{})
	srv := newMCPServerWithMode(service, webfetch.ResolveVersion(version), mode)
	return runAndCloseService(service, true, func() error {
		return serveMCP(ctx, srv, webfetch.ResolveVersion(version), reader, writer)
	})
}

func newMCPServer(service *webfetch.Service, version string) *server.Server {
	return newMCPServerWithLogger(service, version, mcpToolModeFull, os.Stderr)
}

func newMCPFullServer(service *webfetch.Service, version string, logger *slog.Logger) *server.Server {
	srv := server.New(mcpServerOptions(version, "Use web_search for public web search and web_fetch to retrieve a URL as clean Markdown."))
	srv.Use(mcpRequestLogger(logger), server.Recovery())
	srv.AddTool(&protocol.Tool{
		Name:         mcpWebSearchTool,
		Title:        "Search the public web",
		Description:  "Search the public web and return normalized titles, URLs, publication dates, and highlights.",
		InputSchema:  protocol.JSONSchema(mcpWebSearchInputSchema),
		OutputSchema: protocol.JSONSchema(mcpWebSearchOutputSchema),
	}, mcpSearchHandler(service))
	srv.AddTool(&protocol.Tool{
		Name:         mcpWebFetchTool,
		Title:        "Fetch a web page",
		Description:  "Fetch a URL as clean Markdown, or return the bounded raw response body when raw is true.",
		InputSchema:  protocol.JSONSchema(mcpWebFetchInputSchema),
		OutputSchema: protocol.JSONSchema(mcpWebFetchOutputSchema),
	}, mcpFetchHandler(service))
	return srv
}

func mcpSearchHandler(service *webfetch.Service) server.ToolHandler {
	return func(ctx context.Context, req *server.CallRequest) (protocol.ToolResponse, error) {
		result, _, err := service.Dispatch(ctx, webfetch.WireSearchTool, req.Params.Arguments)
		if err != nil {
			return protocol.NewToolResultError(err.Error()), nil
		}
		return mcpJSONResult(result)
	}
}

func mcpFetchHandler(service *webfetch.Service) server.ToolHandler {
	return func(ctx context.Context, req *server.CallRequest) (protocol.ToolResponse, error) {
		var args mcpFetchArguments
		raw, err := json.Marshal(req.Params.Arguments)
		if err != nil {
			return protocol.NewToolResultError(fmt.Sprintf("web_fetch arguments must be a JSON object: %v", err)), nil
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return protocol.NewToolResultError(fmt.Sprintf("web_fetch arguments must be a JSON object: %v", err)), nil
		}
		if args.URL == "" {
			return protocol.NewToolResultError("web_fetch: 'url' is required"), nil
		}
		if args.Raw && !mcpFormatIsMarkdown(args.Format) {
			return protocol.NewToolResultError("web_fetch: raw is only compatible with format markdown"), nil
		}
		limits, err := newOutputLimits(args.MaxBytes, args.MaxLines)
		if err != nil {
			return protocol.NewToolResultError("web_fetch: " + err.Error()), nil
		}
		document, err := service.Fetch(ctx, args.fetchRequest())
		if err != nil {
			return protocol.NewToolResultError(err.Error()), nil
		}
		result, err := mcpFetchResultFromDocument(document, args, limits)
		if err != nil {
			return protocol.NewToolResultError("web_fetch: " + err.Error()), nil
		}
		return mcpJSONResult(result)
	}
}

type mcpFetchArguments struct {
	URL        string `json:"url"`
	Raw        bool   `json:"raw,omitempty"`
	Reader     string `json:"reader,omitempty"`
	Render     string `json:"render,omitempty"`
	RenderWait string `json:"render_wait,omitempty"`
	Format     string `json:"format,omitempty"`
	StartLine  int    `json:"start_line,omitempty"`
	MaxBytes   int    `json:"max_bytes,omitempty"`
	MaxLines   int    `json:"max_lines,omitempty"`
}

func (args mcpFetchArguments) fetchRequest() webfetch.FetchRequest {
	return webfetch.FetchRequest{
		URL:        args.URL,
		Raw:        args.Raw,
		Reader:     args.Reader,
		Render:     args.Render,
		RenderWait: args.RenderWait,
	}
}

type mcpFetchResult struct {
	URL           string   `json:"url"`
	FinalURL      string   `json:"final_url,omitempty"`
	StatusCode    int      `json:"status_code"`
	ContentType   string   `json:"content_type,omitempty"`
	Title         string   `json:"title,omitempty"`
	Description   string   `json:"description,omitempty"`
	Domain        string   `json:"domain,omitempty"`
	Favicon       string   `json:"favicon,omitempty"`
	Image         string   `json:"image,omitempty"`
	Language      string   `json:"language,omitempty"`
	Published     string   `json:"published,omitempty"`
	Author        string   `json:"author,omitempty"`
	Site          string   `json:"site,omitempty"`
	WordCount     int      `json:"word_count"`
	Extractor     string   `json:"extractor,omitempty"`
	Source        string   `json:"source,omitempty"`
	Rendered      bool     `json:"rendered,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
	Format        string   `json:"format,omitempty"`
	Content       string   `json:"content"`
	Truncated     bool     `json:"truncated,omitempty"`
	TruncatedBy   string   `json:"truncated_by,omitempty"`
	TotalBytes    int      `json:"total_bytes,omitempty"`
	OutputBytes   int      `json:"output_bytes,omitempty"`
	TotalLines    int      `json:"total_lines,omitempty"`
	OutputLines   int      `json:"output_lines,omitempty"`
	MaxBytes      int      `json:"max_bytes,omitempty"`
	MaxLines      int      `json:"max_lines,omitempty"`
	StartLine     int      `json:"start_line,omitempty"`
	NextStartLine int      `json:"next_start_line,omitempty"`
}

func mcpFetchResultFromDocument(document webfetch.Document, args mcpFetchArguments, limits outputLimits) (mcpFetchResult, error) {
	projection, err := projectMCPContent(document.Content, args.Format, args.StartLine, limits)
	if err != nil {
		return mcpFetchResult{}, err
	}
	result := mcpFetchResult{
		URL:           document.URL,
		FinalURL:      document.FinalURL,
		StatusCode:    document.StatusCode,
		ContentType:   document.ContentType,
		Title:         document.Title,
		Description:   document.Description,
		Domain:        document.Domain,
		Favicon:       document.Favicon,
		Image:         document.Image,
		Language:      document.Language,
		Published:     document.Published,
		Author:        document.Author,
		Site:          document.Site,
		WordCount:     document.WordCount,
		Extractor:     document.Extractor,
		Source:        document.Source,
		Rendered:      document.Rendered,
		Warnings:      document.Warnings,
		Format:        projection.Format,
		Content:       projection.Content,
		Truncated:     projection.Truncated,
		TruncatedBy:   projection.TruncatedBy,
		TotalBytes:    projection.TotalBytes,
		OutputBytes:   projection.OutputBytes,
		TotalLines:    projection.TotalLines,
		OutputLines:   projection.OutputLines,
		MaxBytes:      limits.MaxBytes,
		MaxLines:      limits.MaxLines,
		StartLine:     projection.StartLine,
		NextStartLine: projection.NextStartLine,
	}
	if normalizeMCPFormat(args.Format) == "" {
		result.Format = ""
	}
	return result, nil
}

func mcpJSONResult(value any) (protocol.ToolResponse, error) {
	result, err := protocol.NewToolResultStructured(value)
	if err != nil {
		return nil, fmt.Errorf("encode MCP tool result: %w", err)
	}
	return result, nil
}

var mcpWebSearchInputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"query": map[string]any{
			"type":        "string",
			"description": "Natural-language web search query.",
		},
		"max_results": map[string]any{
			"type":        "integer",
			"description": "Maximum number of results to return. Defaults to 10.",
			"minimum":     1,
			"maximum":     50,
			"default":     10,
		},
		"category": map[string]any{
			"type":        "string",
			"description": "Optional provider category such as news.",
		},
		"include_domains": map[string]any{
			"type":        "array",
			"description": "Optional hostname filters such as example.com.",
			"items":       map[string]any{"type": "string"},
			"maxItems":    50,
		},
		"start_published_date": map[string]any{
			"type":        "string",
			"description": "Optional lower publication-date bound in RFC3339 or YYYY-MM-DD form.",
		},
		"include_highlights": map[string]any{
			"type":        "boolean",
			"description": "Request provider-generated highlights. Defaults to true.",
			"default":     true,
		},
		"highlight_sentences": map[string]any{
			"type":        "integer",
			"description": "Number of highlight sentences to request. Defaults to 3.",
			"minimum":     1,
			"maximum":     10,
			"default":     3,
		},
	},
	"required": []string{"query"},
}

var mcpWebSearchOutputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"query":    map[string]any{"type": "string"},
		"provider": map[string]any{"type": "string"},
		"results": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":        map[string]any{"type": "string"},
					"url":          map[string]any{"type": "string"},
					"description":  map[string]any{"type": "string"},
					"published_at": map[string]any{"type": "string"},
					"highlights": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
					},
				},
				"required": []string{"title", "url"},
			},
		},
	},
	"required": []string{"query", "results"},
}

var mcpWebFetchInputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"url": map[string]any{
			"type":        "string",
			"description": "Absolute HTTP or HTTPS URL to fetch.",
		},
		"raw": map[string]any{
			"type":        "boolean",
			"description": "Return the bounded raw response body instead of clean Markdown.",
			"default":     false,
		},
		"reader": map[string]any{
			"type":        "string",
			"description": "Optional reader: jina, defuddle, or auto.",
			"enum":        []string{"jina", "defuddle", "auto"},
		},
		"render": map[string]any{
			"type":        "string",
			"description": "Browser rendering policy for Defuddle extraction. Defaults to never.",
			"enum":        []string{"never", "auto", "always"},
			"default":     "never",
		},
		"render_wait": map[string]any{
			"type":        "string",
			"description": "Browser wait strategy when rendering. Omitted uses load.",
			"enum":        []string{"load", "networkidle"},
		},
		"format": map[string]any{
			"type":        "string",
			"description": "Content projection: markdown, headings, or links. Defaults to markdown.",
			"enum":        []string{"markdown", "headings", "links"},
			"default":     defaultMCPFetchFormat,
		},
		"start_line": map[string]any{
			"type":        "integer",
			"description": "Zero-based line offset within the selected projection.",
			"minimum":     0,
			"default":     0,
		},
		"max_bytes": map[string]any{
			"type":        "integer",
			"description": "Optional output limit in UTF-8 bytes; zero means unlimited.",
			"minimum":     0,
			"default":     0,
		},
		"max_lines": map[string]any{
			"type":        "integer",
			"description": "Optional output limit in lines; zero means unlimited.",
			"minimum":     0,
			"default":     0,
		},
	},
	"required": []string{"url"},
}

var mcpWebFetchOutputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"url":             map[string]any{"type": "string"},
		"final_url":       map[string]any{"type": "string"},
		"status_code":     map[string]any{"type": "integer"},
		"content_type":    map[string]any{"type": "string"},
		"title":           map[string]any{"type": "string"},
		"description":     map[string]any{"type": "string"},
		"domain":          map[string]any{"type": "string"},
		"favicon":         map[string]any{"type": "string"},
		"image":           map[string]any{"type": "string"},
		"language":        map[string]any{"type": "string"},
		"published":       map[string]any{"type": "string"},
		"author":          map[string]any{"type": "string"},
		"site":            map[string]any{"type": "string"},
		"word_count":      map[string]any{"type": "integer"},
		"extractor":       map[string]any{"type": "string"},
		"source":          map[string]any{"type": "string"},
		"rendered":        map[string]any{"type": "boolean"},
		"warnings":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"content":         map[string]any{"type": "string"},
		"truncated":       map[string]any{"type": "boolean"},
		"truncated_by":    map[string]any{"type": "string", "enum": []string{"bytes", "lines", "offset"}},
		"total_bytes":     map[string]any{"type": "integer"},
		"output_bytes":    map[string]any{"type": "integer"},
		"total_lines":     map[string]any{"type": "integer"},
		"output_lines":    map[string]any{"type": "integer"},
		"max_bytes":       map[string]any{"type": "integer"},
		"max_lines":       map[string]any{"type": "integer"},
		"format":          map[string]any{"type": "string", "enum": []string{"markdown", "headings", "links"}},
		"start_line":      map[string]any{"type": "integer"},
		"next_start_line": map[string]any{"type": "integer"},
	},
	"required": []string{"url", "status_code", "word_count", "content"},
}
