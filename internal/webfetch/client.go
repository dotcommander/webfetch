package webfetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxAttempts     = 3
	maxRedirectHops = 10
	maxErrorBody    = 512
	baseRetryWait   = 150 * time.Millisecond
	maxRetryWait    = 2 * time.Second
)

var (
	errBodyTooLarge  = ErrTooLarge
	errRedirectLimit = errors.New("redirect limit exceeded")
)

type hostResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type contextDialer func(context.Context, string, string) (net.Conn, error)

type Client struct {
	httpClient           *http.Client
	maxBodyBytes         int64
	allowPrivateNetworks bool
	userAgent            string
	resolver             hostResolver
	dialer               contextDialer
}

func NewClient(cfg ClientConfig) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxBodyBytes := cfg.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	userAgent := strings.TrimSpace(cfg.UserAgent)
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 100
	transport.IdleConnTimeout = 90 * time.Second

	client := &Client{
		maxBodyBytes:         maxBodyBytes,
		allowPrivateNetworks: cfg.AllowPrivateNetworks,
		userAgent:            userAgent,
		resolver:             net.DefaultResolver,
		dialer:               (&net.Dialer{}).DialContext,
	}
	transport.DialContext = client.dialContext
	client.httpClient = &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: client.checkRedirect,
	}
	return client
}

func (c *Client) Get(ctx context.Context, rawURL string, headers http.Header) (httpResponse, error) {
	return c.Do(ctx, http.MethodGet, rawURL, headers, nil)
}

func (c *Client) Do(ctx context.Context, method, rawURL string, headers http.Header, body []byte) (httpResponse, error) {
	target, err := validateTarget(ctx, rawURL, c.allowPrivateNetworks)
	if err != nil {
		return httpResponse{}, err
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		var requestBody io.Reader
		if body != nil {
			requestBody = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, target.String(), requestBody)
		if err != nil {
			return httpResponse{}, fmt.Errorf("create request: %w", newCodedError(err, ErrorCodeInvalidURL, "provide a valid http or https URL"))
		}
		if headers == nil {
			req.Header = make(http.Header)
		} else {
			req.Header = headers.Clone()
		}
		req.Header.Set("User-Agent", c.userAgent)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if ctx.Err() != nil {
				return httpResponse{}, codeContextError(ctx.Err())
			}
			if errors.Is(err, errRedirectLimit) {
				return httpResponse{}, newCodedError(
					fmt.Errorf("%s %s: %w", method, target, err),
					ErrorCodeUpstreamHTTP,
					"check the redirect target or use a URL with fewer redirects",
				)
			}
			var coded *CodedError
			if errors.As(err, &coded) {
				return httpResponse{}, err
			}
			if attempt == maxAttempts {
				return httpResponse{}, fmt.Errorf("GET %s: %w", target, newCodedError(err, ErrorCodeUpstreamHTTP, "check the URL and network connectivity, then retry"))
			}
			if err := waitForRetry(ctx, retryDelay(attempt, nil)); err != nil {
				return httpResponse{}, codeContextError(err)
			}
			continue
		}

		body, readErr := readBounded(resp.Body, c.maxBodyBytes)
		closeErr := resp.Body.Close()
		if readErr != nil {
			if errors.Is(readErr, ErrTooLarge) {
				readErr = newCodedError(readErr, ErrorCodeResponseTooLarge, "reduce the response or configure a larger response limit")
			}
			return httpResponse{}, fmt.Errorf("read %s response: %w", target, readErr)
		}
		if closeErr != nil {
			return httpResponse{}, fmt.Errorf("close %s response: %w", target, closeErr)
		}

		if shouldRetry(resp.StatusCode) && attempt < maxAttempts {
			if err := waitForRetry(ctx, retryDelay(attempt, resp)); err != nil {
				return httpResponse{}, codeContextError(err)
			}
			continue
		}

		result := httpResponse{
			URL:        target.String(),
			StatusCode: resp.StatusCode,
			Header:     resp.Header.Clone(),
			Body:       body,
		}
		if resp.Request != nil && resp.Request.URL != nil {
			result.URL = resp.Request.URL.String()
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			httpErr := &HTTPError{
				URL:        result.URL,
				StatusCode: resp.StatusCode,
				Status:     resp.Status,
				Body:       truncate(string(body), maxErrorBody),
			}
			return httpResponse{}, newCodedError(httpErr, httpErrorCode(resp.StatusCode), httpErrorSuggestion(resp.StatusCode))
		}
		return result, nil
	}

	return httpResponse{}, newCodedError(fmt.Errorf("%s %s: retry attempts exhausted", method, target), ErrorCodeUpstreamHTTP, "retry the request later")
}

func httpErrorCode(statusCode int) string {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrorCodeAuthentication
	case http.StatusTooManyRequests:
		return ErrorCodeRateLimited
	default:
		return ErrorCodeUpstreamHTTP
	}
}

func validateTarget(ctx context.Context, rawURL string, allowPrivateNetworks bool) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, newCodedError(errors.New("URL is required"), ErrorCodeInvalidURL, "provide an absolute http or https URL")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, newCodedError(fmt.Errorf("parse URL: %w", err), ErrorCodeInvalidURL, "provide a valid absolute http or https URL")
	}
	if err := validateURL(ctx, u, allowPrivateNetworks); err != nil {
		return nil, err
	}
	return u, nil
}

func validateURL(ctx context.Context, u *url.URL, allowPrivateNetworks bool) error {
	if u == nil {
		return newCodedError(errors.New("URL is required"), ErrorCodeInvalidURL, "provide an absolute http or https URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return newCodedError(fmt.Errorf("unsupported URL scheme %q: only http and https are allowed", u.Scheme), ErrorCodeInvalidURL, "use an http or https URL")
	}
	if u.Hostname() == "" {
		return newCodedError(errors.New("URL host is required"), ErrorCodeInvalidURL, "include a hostname in the URL")
	}
	if u.User != nil {
		return newCodedError(errors.New("URLs with embedded credentials are not allowed"), ErrorCodeInvalidURL, "remove embedded username and password credentials from the URL")
	}
	if allowPrivateNetworks {
		return nil
	}
	return validateHost(u.Hostname())
}

func readBounded(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errBodyTooLarge
	}
	return data, nil
}

func shouldRetry(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func httpErrorSuggestion(statusCode int) string {
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return "check the target URL and provider credentials"
	}
	if statusCode == http.StatusTooManyRequests {
		return "wait and retry with fewer requests"
	}
	if shouldRetry(statusCode) {
		return "retry the request later or check the upstream service status"
	}
	return "check the target URL and upstream response"
}

func retryDelay(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if value := strings.TrimSpace(resp.Header.Get("Retry-After")); value != "" {
			if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
				return min(time.Duration(seconds)*time.Second, maxRetryWait)
			}
			if when, err := http.ParseTime(value); err == nil {
				return min(max(when.Sub(time.Now()), 0), maxRetryWait)
			}
		}
	}
	wait := baseRetryWait * time.Duration(1<<(attempt-1))
	return min(wait, maxRetryWait)
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func truncate(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes] + "..."
}
