package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/dotcommander/webfetch/internal/webfetch"
	"github.com/voocel/mcp-sdk-go/protocol"
	"github.com/voocel/mcp-sdk-go/server"
)

const (
	mcpLogLevelEnv     = "WEBFETCH_MCP_LOG_LEVEL"
	mcpListCacheTTLMs  = 60000
	mcpDefaultLogLevel = "off"
)

func newMCPServerWithLogger(service *webfetch.Service, version string, mode mcpToolMode, writer io.Writer) *server.Server {
	logger := newMCPLogger(writer)
	if mode == mcpToolModeCompact {
		return newMCPCompactServerWithLogger(service, version, logger)
	}
	return newMCPFullServer(service, version, logger)
}

func newMCPLogger(writer io.Writer) *slog.Logger {
	if writer == nil {
		writer = os.Stderr
	}
	level, enabled := parseMCPLogLevel(os.Getenv(mcpLogLevelEnv))
	if !enabled {
		writer = io.Discard
	}
	return slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: level}))
}

func parseMCPLogLevel(value string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", mcpDefaultLogLevel:
		return slog.LevelInfo, false
	case "error":
		return slog.LevelError, true
	case "info":
		return slog.LevelInfo, true
	case "debug":
		return slog.LevelDebug, true
	default:
		return slog.LevelInfo, false
	}
}

func mcpServerOptions(version, instructions string) *server.Options {
	if version == "" {
		version = "dev"
	}
	return &server.Options{
		Impl: protocol.Implementation{
			Name:    "webfetch",
			Title:   "webfetch",
			Version: version,
		},
		Instructions: instructions,
		ListCache: protocol.CacheControl{
			TTLMs:      mcpListCacheTTLMs,
			CacheScope: protocol.CacheScopePublic,
		},
	}
}

func mcpRequestLogger(logger *slog.Logger) server.Middleware {
	return func(next server.RawHandler) server.RawHandler {
		return func(ctx context.Context, req *server.Request) (protocol.Result, error) {
			started := time.Now()
			result, err := next(ctx, req)
			attrs := []any{
				"method", req.Method(),
				"request_id", fmt.Sprint(req.ID()),
				"elapsed_ms", time.Since(started).Milliseconds(),
				"outcome", mcpLogOutcome(err),
			}
			if tool := mcpRequestTool(req); tool != "" {
				attrs = append(attrs, "tool", tool)
			}
			if logger.Enabled(ctx, slog.LevelDebug) {
				if info := req.ClientInfo(); info != nil {
					attrs = append(attrs, "client", info.Name, "client_version", info.Version)
				}
				attrs = append(attrs, "protocol_version", req.ProtocolVersion())
			}
			if err != nil {
				attrs = append(attrs, "error_code", mcpErrorCode(err))
			}
			level := slog.LevelInfo
			if err != nil {
				level = slog.LevelError
			}
			logger.Log(ctx, level, "mcp request", attrs...)
			return result, err
		}
	}
}

func mcpRequestTool(req *server.Request) string {
	if req.Method() != protocol.MethodToolsCall {
		return ""
	}
	var params struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(req.RawParams(), &params) != nil {
		return ""
	}
	return params.Name
}

func mcpLogOutcome(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

func mcpErrorCode(err error) string {
	var coded *webfetch.CodedError
	if errors.As(err, &coded) && coded.Code != "" {
		return coded.Code
	}
	return "internal"
}
