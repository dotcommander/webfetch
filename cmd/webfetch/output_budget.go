package main

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

type outputLimits struct {
	MaxBytes int
	MaxLines int
}

type contentProjection struct {
	Content     string
	Truncated   bool
	TruncatedBy string
	TotalBytes  int
	OutputBytes int
	TotalLines  int
	OutputLines int
}

func newOutputLimits(maxBytes, maxLines int) (outputLimits, error) {
	if maxBytes < 0 {
		return outputLimits{}, fmt.Errorf("max-bytes must be non-negative")
	}
	if maxLines < 0 {
		return outputLimits{}, fmt.Errorf("max-lines must be non-negative")
	}
	return outputLimits{MaxBytes: maxBytes, MaxLines: maxLines}, nil
}

func projectContent(content string, limits outputLimits) contentProjection {
	projection := contentProjection{
		Content:    content,
		TotalBytes: len(content),
		TotalLines: contentLineCount(content),
	}
	if (limits.MaxBytes <= 0 || projection.TotalBytes <= limits.MaxBytes) &&
		(limits.MaxLines <= 0 || projection.TotalLines <= limits.MaxLines) {
		projection.OutputBytes = projection.TotalBytes
		projection.OutputLines = projection.TotalLines
		return projection
	}

	projected := content
	if limits.MaxLines > 0 && projection.TotalLines > limits.MaxLines {
		projected = prefixByLines(projected, limits.MaxLines)
		projection.TruncatedBy = "lines"
	}
	if limits.MaxBytes > 0 && len(projected) > limits.MaxBytes {
		projected = prefixByBytes(projected, limits.MaxBytes)
		projection.TruncatedBy = "bytes"
	}
	if projected == content {
		projection.TruncatedBy = ""
	}
	projection.Content = projected
	projection.OutputBytes = len(projected)
	projection.OutputLines = contentLineCount(projected)
	projection.Truncated = projected != content
	return projection
}

func contentLineCount(content string) int {
	if content == "" {
		return 0
	}
	lines := strings.Split(content, "\n")
	if lines[len(lines)-1] == "" {
		return len(lines) - 1
	}
	return len(lines)
}

func prefixByLines(content string, maxLines int) string {
	if maxLines <= 0 || contentLineCount(content) <= maxLines {
		return content
	}
	lines := strings.SplitAfter(content, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "")
}

func prefixByBytes(content string, maxBytes int) string {
	if maxBytes <= 0 || len(content) <= maxBytes {
		return content
	}
	prefix := utf8Prefix(content, maxBytes)
	if newline := strings.LastIndexByte(prefix, '\n'); newline >= 0 {
		return prefix[:newline+1]
	}
	return prefix
}

func utf8Prefix(content string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(content) <= maxBytes {
		return content
	}
	prefix := content[:maxBytes]
	for len(prefix) > 0 && !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix
}

func truncationMarker(projection contentProjection) string {
	return fmt.Sprintf("[truncated: showing %d/%d lines, %d/%d bytes (%s)]",
		projection.OutputLines,
		projection.TotalLines,
		projection.OutputBytes,
		projection.TotalBytes,
		projection.TruncatedBy,
	)
}
