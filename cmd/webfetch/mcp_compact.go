package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/dotcommander/webfetch/internal/webfetch"
	"github.com/voocel/mcp-sdk-go/protocol"
	"github.com/voocel/mcp-sdk-go/server"
)

type mcpToolMode string

const (
	mcpToolModeFull    mcpToolMode = "full"
	mcpToolModeCompact mcpToolMode = "compact"
	mcpCompactTool                 = "webfetch"
	mcpToolModeEnv                 = "WEBFETCH_MCP_TOOL_MODE"
)

func mcpToolModeFromArgs(args []string) (mcpToolMode, error) {
	mode := mcpToolMode(strings.ToLower(strings.TrimSpace(os.Getenv(mcpToolModeEnv))))
	switch mode {
	case "":
		mode = mcpToolModeFull
	case mcpToolModeFull, mcpToolModeCompact:
	default:
		return "", fmt.Errorf("%s must be full or compact", mcpToolModeEnv)
	}
	for _, arg := range args {
		if arg != "--compact" {
			return "", fmt.Errorf("--mcp accepts only --compact; configure providers with environment variables")
		}
		mode = mcpToolModeCompact
	}
	return mode, nil
}

func newMCPServerWithMode(service *webfetch.Service, version string, mode mcpToolMode) *server.Server {
	return newMCPServerWithLogger(service, version, mode, os.Stderr)
}

func newMCPCompactServer(service *webfetch.Service, version string) *server.Server {
	return newMCPCompactServerWithLogger(service, version, newMCPLogger(os.Stderr))
}

func newMCPCompactServerWithLogger(service *webfetch.Service, version string, logger *slog.Logger) *server.Server {
	srv := server.New(mcpServerOptions(version, "Use the webfetch tool with operation fetch or search. The compact surface keeps one tool definition in the model context."))
	srv.Use(mcpRequestLogger(logger), server.Recovery())
	srv.AddTool(&protocol.Tool{
		Name:         mcpCompactTool,
		Title:        "Fetch or search the web",
		Description:  "Compact webfetch router. Set operation to fetch or search and provide the operation-specific input object.",
		InputSchema:  protocol.JSONSchema(mcpCompactInputSchema),
		OutputSchema: protocol.JSONSchema(mcpCompactOutputSchema),
	}, mcpCompactHandler(service))
	return srv
}

type mcpCompactArguments struct {
	Operation string         `json:"operation"`
	Input     map[string]any `json:"input"`
}

type mcpCompactToolResult struct {
	Operation string `json:"operation"`
	Result    any    `json:"result"`
}

func mcpCompactHandler(service *webfetch.Service) server.ToolHandler {
	return func(ctx context.Context, req *server.CallRequest) (protocol.ToolResponse, error) {
		var args mcpCompactArguments
		raw, err := json.Marshal(req.Params.Arguments)
		if err != nil {
			return protocol.NewToolResultError(fmt.Sprintf("webfetch arguments must be a JSON object: %v", err)), nil
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return protocol.NewToolResultError(fmt.Sprintf("webfetch arguments must be an object with operation and input: %v", err)), nil
		}
		if args.Input == nil {
			return protocol.NewToolResultError("webfetch: 'input' is required"), nil
		}
		switch strings.TrimSpace(args.Operation) {
		case "fetch":
			return mcpCompactFetch(ctx, service, args.Input)
		case "search":
			result, _, err := service.Dispatch(ctx, webfetch.WireSearchTool, args.Input)
			if err != nil {
				return protocol.NewToolResultError(err.Error()), nil
			}
			return mcpJSONResult(mcpCompactToolResult{Operation: "search", Result: result})
		default:
			return protocol.NewToolResultError("webfetch: 'operation' must be 'fetch' or 'search'"), nil
		}
	}
}

func mcpCompactFetch(ctx context.Context, service *webfetch.Service, input map[string]any) (protocol.ToolResponse, error) {
	var args mcpFetchArguments
	raw, err := json.Marshal(input)
	if err != nil {
		return protocol.NewToolResultError(fmt.Sprintf("webfetch fetch input must be a JSON object: %v", err)), nil
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return protocol.NewToolResultError(fmt.Sprintf("webfetch fetch input must be an object: %v", err)), nil
	}
	if args.URL == "" {
		return protocol.NewToolResultError("webfetch fetch: 'url' is required"), nil
	}
	if args.Raw && !mcpFormatIsMarkdown(args.Format) {
		return protocol.NewToolResultError("webfetch fetch: raw is only compatible with format markdown"), nil
	}
	limits, err := newOutputLimits(args.MaxBytes, args.MaxLines)
	if err != nil {
		return protocol.NewToolResultError("webfetch fetch: " + err.Error()), nil
	}
	document, err := service.Fetch(ctx, args.fetchRequest())
	if err != nil {
		return protocol.NewToolResultError(err.Error()), nil
	}
	result, err := mcpFetchResultFromDocument(document, args, limits)
	if err != nil {
		return protocol.NewToolResultError("webfetch fetch: " + err.Error()), nil
	}
	return mcpJSONResult(mcpCompactToolResult{
		Operation: "fetch",
		Result:    result,
	})
}

var mcpCompactInputSchema = map[string]any{
	"$schema": "https://json-schema.org/draft/2020-12/schema",
	"title":   "webfetch compact router input",
	"type":    "object",
	"oneOf": []any{
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"operation": map[string]any{"const": "fetch"},
				"input":     map[string]any{"$ref": "#/$defs/fetch_input"},
			},
			"required":             []string{"operation", "input"},
			"additionalProperties": false,
		},
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"operation": map[string]any{"const": "search"},
				"input":     map[string]any{"$ref": "#/$defs/search_input"},
			},
			"required":             []string{"operation", "input"},
			"additionalProperties": false,
		},
	},
	"$defs": map[string]any{
		"fetch_input":  mcpWebFetchInputSchema,
		"search_input": mcpWebSearchInputSchema,
	},
}

var mcpCompactOutputSchema = map[string]any{
	"$schema": "https://json-schema.org/draft/2020-12/schema",
	"title":   "webfetch compact router output",
	"type":    "object",
	"oneOf": []any{
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"operation": map[string]any{"const": "fetch"},
				"result":    map[string]any{"$ref": "#/$defs/fetch_result"},
			},
			"required":             []string{"operation", "result"},
			"additionalProperties": false,
		},
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"operation": map[string]any{"const": "search"},
				"result":    map[string]any{"$ref": "#/$defs/search_result"},
			},
			"required":             []string{"operation", "result"},
			"additionalProperties": false,
		},
	},
	"$defs": map[string]any{
		"fetch_result":  mcpWebFetchOutputSchema,
		"search_result": mcpWebSearchOutputSchema,
	},
}
