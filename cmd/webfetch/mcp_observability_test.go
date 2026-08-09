package main

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/dotcommander/webfetch/internal/webfetch"
)

func TestParseMCPLogLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input   string
		level   slog.Level
		enabled bool
	}{
		{input: "", level: slog.LevelInfo},
		{input: "off", level: slog.LevelInfo},
		{input: " ERROR ", level: slog.LevelError, enabled: true},
		{input: "info", level: slog.LevelInfo, enabled: true},
		{input: "DEBUG", level: slog.LevelDebug, enabled: true},
		{input: "verbose", level: slog.LevelInfo},
	}
	for _, tt := range tests {
		level, enabled := parseMCPLogLevel(tt.input)
		if level != tt.level || enabled != tt.enabled {
			t.Errorf("parseMCPLogLevel(%q) = (%s, %t), want (%s, %t)", tt.input, level, enabled, tt.level, tt.enabled)
		}
	}
}

func TestMCPErrorCode(t *testing.T) {
	t.Parallel()
	coded := webfetch.NewCodedError(errors.New("bad input"), webfetch.ErrorCodeInvalidArgument, "fix it")
	if got := mcpErrorCode(coded); got != webfetch.ErrorCodeInvalidArgument {
		t.Fatalf("coded error = %q", got)
	}
	if got := mcpErrorCode(errors.New("plain")); got != "internal" {
		t.Fatalf("plain error = %q", got)
	}
}
