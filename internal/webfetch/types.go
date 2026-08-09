package webfetch

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultTimeout               = 30 * time.Second
	DefaultMaxBodyBytes          = 8 << 20
	DefaultRenderTimeout         = 30 * time.Second
	DefaultRenderMaxConcurrency  = 2
	DefaultRenderMaxRequests     = 100
	DefaultRenderMaxNetworkBytes = 32 << 20
	DefaultSearchLimit           = 5
	MaxSearchLimit               = 50
	DefaultReaderEndpoint        = "https://r.jina.ai"
	DefaultSearchEndpoint        = "https://api.search.brave.com/res/v1/web/search"
	DefaultExaEndpoint           = "https://api.exa.ai/search"
	DefaultUserAgent             = "webfetch/0.1"
)

// Config controls provider endpoints and HTTP safety limits.
type Config struct {
	JinaAPIKey            string
	BraveAPIKey           string
	ExaAPIKey             string
	ReaderMode            string
	ReaderEndpoint        string
	SearchEndpoint        string
	ExaSearchEndpoint     string
	SearchProvider        string
	URLCacheTTL           time.Duration
	URLCacheDir           string
	Timeout               time.Duration
	MaxBodyBytes          int64
	AllowPrivateNetworks  bool
	RenderTimeout         time.Duration
	RenderMaxConcurrency  int
	RenderMaxRequests     int
	RenderMaxNetworkBytes int64
	RenderMaxHTMLBytes    int64
	ChromePath            string
}

// ClientConfig controls the shared HTTP client.
type ClientConfig struct {
	Timeout              time.Duration
	MaxBodyBytes         int64
	AllowPrivateNetworks bool
	UserAgent            string
}

// FetchRequest describes one URL fetch.
type FetchRequest struct {
	URL        string
	Raw        bool
	Reader     string
	Render     string
	RenderWait string
}

// Document is the normalized result of a URL fetch.
type Document struct {
	URL         string
	FinalURL    string
	StatusCode  int
	ContentType string
	Title       string
	Description string
	Domain      string
	Favicon     string
	Image       string
	Language    string
	Published   string
	Author      string
	Site        string
	WordCount   int
	Extractor   string
	Source      string
	Rendered    bool
	Warnings    []string
	Content     string
}

// SearchRequest describes one web search.
type SearchRequest struct {
	Query              string
	Limit              int
	Category           string
	IncludeDomains     []string
	StartPublishedDate string
	IncludeHighlights  bool
	HighlightSentences int
}

// SearchResponse is the normalized search result.
type SearchResponse struct {
	Query    string         `json:"query"`
	Provider string         `json:"provider,omitempty"`
	Results  []SearchResult `json:"results"`
}

// SearchResult is one search hit.
type SearchResult struct {
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	Description string   `json:"description,omitempty"`
	PublishedAt string   `json:"published_at,omitempty"`
	Highlights  []string `json:"highlights,omitempty"`
}

type httpResponse struct {
	URL        string
	StatusCode int
	Header     http.Header
	Body       []byte
}

// HTTPError represents a bounded non-success HTTP response.
type HTTPError struct {
	URL        string
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPError) Error() string {
	if strings.TrimSpace(e.Body) == "" {
		return fmt.Sprintf("%s: %s", e.URL, e.Status)
	}
	return fmt.Sprintf("%s: %s: %s", e.URL, e.Status, e.Body)
}
