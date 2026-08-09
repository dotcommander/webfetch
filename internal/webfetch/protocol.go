package webfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const WireSearchTool = "web_search"

// WireRequest is one machine-protocol request. Args accepts both an object
// and the JSON-encoded string form emitted by some clients.
type WireRequest struct {
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args"`
	Client    string         `json:"client,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
}

func (r *WireRequest) UnmarshalJSON(data []byte) error {
	type rawRequest struct {
		Tool      string          `json:"tool"`
		Args      json.RawMessage `json:"args"`
		Client    string          `json:"client,omitempty"`
		RequestID string          `json:"request_id,omitempty"`
	}
	var raw rawRequest
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.Tool = raw.Tool
	r.Client = raw.Client
	r.RequestID = raw.RequestID
	if len(raw.Args) == 0 || string(raw.Args) == "null" {
		r.Args = nil
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw.Args, &args); err == nil {
		r.Args = args
		return nil
	}
	var encoded string
	if err := json.Unmarshal(raw.Args, &encoded); err != nil {
		return errors.New("args must be a JSON object")
	}
	if err := json.Unmarshal([]byte(encoded), &args); err != nil {
		return errors.New("args is a JSON-encoded string but does not contain a JSON object")
	}
	r.Args = args
	return nil
}

// WireResponse is the one-shot machine-protocol envelope.
type WireResponse struct {
	OK         bool           `json:"ok"`
	Result     any            `json:"result,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
	Error      string         `json:"error,omitempty"`
	Suggestion string         `json:"suggestion,omitempty"`
	ErrorCode  string         `json:"error_code,omitempty"`
	RequestID  string         `json:"request_id,omitempty"`
}

// Dispatch validates and executes one supported machine-protocol tool call.
func (s *Service) Dispatch(ctx context.Context, tool string, args map[string]any) (SearchResponse, map[string]any, error) {
	if tool != WireSearchTool {
		return SearchResponse{}, nil, newCodedError(fmt.Errorf("unknown tool: %s", tool), ErrorCodeInvalidArgument, `use {"tool":"web_search","args":{"query":"..."}}`)
	}
	req, err := ParseSearchRequest(args)
	if err != nil {
		return SearchResponse{}, nil, err
	}
	result, err := s.Search(ctx, req)
	if err != nil {
		return SearchResponse{}, nil, err
	}
	return result, map[string]any{
		"provider":              result.Provider,
		"result_count":          len(result.Results),
		"requested_max_results": req.Limit,
	}, nil
}
