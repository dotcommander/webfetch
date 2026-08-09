package webfetch

import (
	"regexp"
	"strings"
)

type shellClass uint8

const (
	shellClassStatic shellClass = iota
	shellClassAmbiguous
	shellClassLikely
)

var (
	knownAppMarkers = regexp.MustCompile(`(?i)(?:id|class)=["'][^"']*(?:app|root|__next|__nuxt|react)[^"']*["']|__next_data__|__nuxt__|data-reactroot|data-vue-meta`)
	scriptTag       = regexp.MustCompile(`(?i)<script\b`)
	noscriptTag     = regexp.MustCompile(`(?i)<noscript\b`)
	textTag         = regexp.MustCompile(`(?is)<(?:script|style|noscript|svg)\b.*?</(?:script|style|noscript|svg)>`)
	markupTag       = regexp.MustCompile(`(?is)<[^>]+>`)
)

// classifyHTML identifies pages that are likely to need a browser before
// extraction. It is intentionally conservative: static extraction remains the
// default, and only a strong shell signal escalates render=auto.
func classifyHTML(html string) shellClass {
	trimmed := strings.TrimSpace(html)
	if trimmed == "" {
		return shellClassLikely
	}
	if knownAppMarkers.MatchString(trimmed) {
		return shellClassLikely
	}

	visible := textTag.ReplaceAllString(trimmed, " ")
	visible = markupTag.ReplaceAllString(visible, " ")
	visible = strings.Join(strings.Fields(visible), " ")
	textBytes := len([]byte(visible))
	scripts := len(scriptTag.FindAllStringIndex(trimmed, -1))
	noscripts := len(noscriptTag.FindAllStringIndex(trimmed, -1))

	// A short document containing several scripts and a noscript fallback is a
	// common SPA shell. Keep this threshold high enough not to render ordinary
	// small static pages.
	if textBytes < 240 && scripts >= 3 {
		return shellClassLikely
	}
	if textBytes < 160 && (scripts >= 1 || noscripts >= 1) {
		return shellClassAmbiguous
	}
	return shellClassStatic
}
