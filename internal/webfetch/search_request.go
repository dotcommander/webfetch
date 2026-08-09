package webfetch

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	defaultProtocolSearchLimit = 10
	defaultHighlightSentences  = 3
)

// ParseSearchRequest validates the provider-neutral arguments used by the
// machine protocol and applies its larger result-limit default.
func ParseSearchRequest(args map[string]any) (SearchRequest, error) {
	query, ok := args["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return SearchRequest{}, newCodedError(fmt.Errorf("web_search: 'query' is required"), ErrorCodeInvalidArgument, `valid shape: {"query":"search terms"}`)
	}
	maxResults, err := intSearchArg(args, "max_results", defaultProtocolSearchLimit)
	if err != nil {
		return SearchRequest{}, err
	}
	category, err := stringSearchArg(args, "category", "")
	if err != nil {
		return SearchRequest{}, err
	}
	domains, err := stringSliceSearchArg(args, "include_domains")
	if err != nil {
		return SearchRequest{}, err
	}
	startDate, err := stringSearchArg(args, "start_published_date", "")
	if err != nil {
		return SearchRequest{}, err
	}
	includeHighlights, err := boolSearchArg(args, "include_highlights", true)
	if err != nil {
		return SearchRequest{}, err
	}
	highlightSentences, err := intSearchArg(args, "highlight_sentences", defaultHighlightSentences)
	if err != nil {
		return SearchRequest{}, err
	}
	return validateSearchRequest(SearchRequest{
		Query:              query,
		Limit:              maxResults,
		Category:           category,
		IncludeDomains:     domains,
		StartPublishedDate: startDate,
		IncludeHighlights:  includeHighlights,
		HighlightSentences: highlightSentences,
	}, defaultProtocolSearchLimit)
}

func normalizeSearchDate(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if timestamp, err := time.Parse("2006-01-02", value); err == nil {
		return timestamp.UTC().Format(time.RFC3339), nil
	}
	if timestamp, err := time.Parse(time.RFC3339, value); err == nil {
		return timestamp.UTC().Format(time.RFC3339), nil
	}
	return "", newCodedError(fmt.Errorf("start_published_date must be RFC3339 or YYYY-MM-DD"), ErrorCodeInvalidArgument, "use a timestamp such as 2026-08-01T00:00:00Z")
}

func decodeJSON(data []byte, target any, name string) error {
	if err := json.Unmarshal(data, target); err != nil {
		return newCodedError(fmt.Errorf("decode %s response: %w", name, err), ErrorCodeProviderResponse, "retry the search or inspect the provider response format")
	}
	return nil
}

func intSearchArg(args map[string]any, name string, defaultValue int) (int, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return defaultValue, nil
	}
	switch number := value.(type) {
	case int:
		return number, nil
	case int64:
		return int(number), nil
	case float64:
		if number == float64(int(number)) {
			return int(number), nil
		}
	case json.Number:
		parsed, err := number.Int64()
		if err == nil {
			return int(parsed), nil
		}
	}
	return 0, newCodedError(fmt.Errorf("web_search: '%s' must be an integer", name), ErrorCodeInvalidArgument, fmt.Sprintf("set '%s' to an integer value", name))
}

func stringSearchArg(args map[string]any, name, defaultValue string) (string, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return defaultValue, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", newCodedError(fmt.Errorf("web_search: '%s' must be a string", name), ErrorCodeInvalidArgument, fmt.Sprintf("set '%s' to a string value", name))
	}
	return strings.TrimSpace(text), nil
}

func boolSearchArg(args map[string]any, name string, defaultValue bool) (bool, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return defaultValue, nil
	}
	boolean, ok := value.(bool)
	if !ok {
		return false, newCodedError(fmt.Errorf("web_search: '%s' must be a boolean", name), ErrorCodeInvalidArgument, fmt.Sprintf("set '%s' to true or false", name))
	}
	return boolean, nil
}

func stringSliceSearchArg(args map[string]any, name string) ([]string, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return nil, nil
	}
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...), nil
	case []any:
		result := make([]string, len(values))
		for i, item := range values {
			text, ok := item.(string)
			if !ok {
				return nil, newCodedError(fmt.Errorf("web_search: '%s' must contain only strings", name), ErrorCodeInvalidArgument, fmt.Sprintf("set '%s' to an array of domain strings", name))
			}
			result[i] = text
		}
		return result, nil
	default:
		return nil, newCodedError(fmt.Errorf("web_search: '%s' must be an array", name), ErrorCodeInvalidArgument, fmt.Sprintf("set '%s' to an array of domain strings", name))
	}
}
