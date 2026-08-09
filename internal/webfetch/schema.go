package webfetch

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// SearchSchema is the OpenAI-compatible function schema for the wire search
// tool.
//
//go:embed schema.json
var SearchSchema string

// ValidateSearchSchema checks the embedded wire-protocol schema contract.
func ValidateSearchSchema() error {
	var definitions []struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal([]byte(SearchSchema), &definitions); err != nil {
		return fmt.Errorf("decode search schema: %w", err)
	}
	if len(definitions) != 1 || definitions[0].Type != "function" || definitions[0].Function.Name != WireSearchTool {
		return fmt.Errorf("search schema must expose exactly %s", WireSearchTool)
	}
	return nil
}
