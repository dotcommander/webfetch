package main

import (
	"fmt"
	"regexp"
	"strings"
)

const defaultMCPFetchFormat = "markdown"

var (
	mcpHeadingPattern  = regexp.MustCompile(`^#{1,6}\s+`)
	mcpLinkPattern     = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)`)
	mcpAutoLinkPattern = regexp.MustCompile(`<((?:https?://|mailto:)[^>]+)>`)
)

type mcpContentProjection struct {
	contentProjection
	Format        string
	StartLine     int
	NextStartLine int
}

func projectMCPContent(content, format string, startLine int, limits outputLimits) (mcpContentProjection, error) {
	format = normalizeMCPFormat(format)
	if format == "" {
		format = defaultMCPFetchFormat
	}
	if startLine < 0 {
		return mcpContentProjection{}, fmt.Errorf("start_line must be non-negative")
	}
	derived, err := deriveMCPContent(content, format)
	if err != nil {
		return mcpContentProjection{}, err
	}
	totalLines := contentLineCount(derived)
	selected := contentFromLine(derived, startLine)
	projection := projectContent(selected, limits)
	projection.TotalBytes = len(derived)
	projection.TotalLines = totalLines
	if startLine > 0 && !projection.Truncated {
		projection.Truncated = true
		projection.TruncatedBy = "offset"
	}
	result := mcpContentProjection{
		contentProjection: projection,
		Format:            format,
		StartLine:         startLine,
	}
	if startLine+projection.OutputLines < totalLines {
		result.NextStartLine = startLine + projection.OutputLines
	}
	return result, nil
}

func normalizeMCPFormat(format string) string {
	return strings.ToLower(strings.TrimSpace(format))
}

func mcpFormatIsMarkdown(format string) bool {
	normalized := normalizeMCPFormat(format)
	return normalized == "" || normalized == defaultMCPFetchFormat
}

func deriveMCPContent(content, format string) (string, error) {
	switch format {
	case defaultMCPFetchFormat:
		return content, nil
	case "headings":
		return extractMCPHeadings(content), nil
	case "links":
		return extractMCPLinks(content), nil
	default:
		return "", fmt.Errorf("format must be markdown, headings, or links")
	}
}

func extractMCPHeadings(content string) string {
	var headings strings.Builder
	for _, line := range strings.SplitAfter(content, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r\n"))
		if mcpHeadingPattern.MatchString(trimmed) {
			headings.WriteString(trimmed)
			headings.WriteByte('\n')
		}
	}
	return headings.String()
}

func extractMCPLinks(content string) string {
	seen := make(map[string]struct{})
	var links strings.Builder
	appendLink := func(link string) {
		link = strings.TrimSpace(link)
		if link == "" {
			return
		}
		if _, ok := seen[link]; ok {
			return
		}
		seen[link] = struct{}{}
		links.WriteString(link)
		links.WriteByte('\n')
	}
	for _, match := range mcpLinkPattern.FindAllStringSubmatch(content, -1) {
		appendLink(match[1])
	}
	for _, match := range mcpAutoLinkPattern.FindAllStringSubmatch(content, -1) {
		appendLink(match[1])
	}
	return links.String()
}

func contentFromLine(content string, startLine int) string {
	if startLine <= 0 || content == "" {
		return content
	}
	lines := strings.SplitAfter(content, "\n")
	if startLine >= len(lines) {
		return ""
	}
	return strings.Join(lines[startLine:], "")
}
