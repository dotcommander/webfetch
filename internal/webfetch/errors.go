package webfetch

import (
	"context"
	"errors"
)

const (
	ErrorCodeInvalidURL         = "invalid_url"
	ErrorCodePrivateNetwork     = "private_network_blocked"
	ErrorCodeUnsupportedReader  = "unsupported_reader"
	ErrorCodeNotHTML            = "not_html"
	ErrorCodeResponseTooLarge   = "response_too_large"
	ErrorCodeUpstreamHTTP       = "upstream_http"
	ErrorCodeAuthentication     = "authentication_failed"
	ErrorCodeRateLimited        = "rate_limited"
	ErrorCodeMissingAPIKey      = "missing_api_key"
	ErrorCodeRequestTimeout     = "request_timeout"
	ErrorCodeCanceled           = "canceled"
	ErrorCodeInvalidArgument    = "invalid_argument"
	ErrorCodeUnsupportedSearch  = "unsupported_search_option"
	ErrorCodeInvalidJSON        = "invalid_json"
	ErrorCodeNoInput            = "no_input"
	ErrorCodeProviderResponse   = "provider_response_invalid"
	ErrorCodeExtraction         = "extraction_failed"
	ErrorCodeReaderFallback     = "reader_fallback_failed"
	ErrorCodeBrowserUnavailable = "browser_unavailable"
	ErrorCodeRenderFailed       = "render_failed"
	ErrorCodeRenderBudget       = "render_budget_exceeded"
)

// CodedError preserves the original error while exposing stable, actionable
// metadata to machine-readable callers.
type CodedError struct {
	Err        error
	Code       string
	Suggestion string
}

func (e *CodedError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *CodedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func newCodedError(err error, code, suggestion string) error {
	if err == nil {
		return nil
	}
	return &CodedError{Err: err, Code: code, Suggestion: suggestion}
}

// NewCodedError wraps err with machine-readable recovery metadata.
func NewCodedError(err error, code, suggestion string) error {
	return newCodedError(err, code, suggestion)
}

func codeContextError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return newCodedError(err, ErrorCodeRequestTimeout, "increase the timeout or retry the request")
	}
	if errors.Is(err, context.Canceled) {
		return newCodedError(err, ErrorCodeCanceled, "retry the request if cancellation was unintended")
	}
	return err
}

var (
	// ErrNotHTML indicates that an HTML reader received a non-HTML response.
	ErrNotHTML = errors.New("response is not HTML")

	// ErrTooLarge indicates that a response exceeded the configured body limit.
	ErrTooLarge = errors.New("response body exceeds configured limit")

	// ErrUnsupportedReader indicates that a fetch request selected an unknown reader.
	ErrUnsupportedReader = errors.New("unsupported reader")
)
