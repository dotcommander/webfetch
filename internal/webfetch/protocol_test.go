package webfetch

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestWireRequestRepairsDoubleEncodedArgs(t *testing.T) {
	var request WireRequest
	if err := json.Unmarshal([]byte(`{"tool":"web_search","args":"{\"query\":\"go\"}","request_id":"req-1"}`), &request); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if request.Tool != WireSearchTool || request.RequestID != "req-1" || request.Args["query"] != "go" {
		t.Fatalf("request = %+v", request)
	}
}

func TestWireRequestRepairsObjectArgsAndNull(t *testing.T) {
	var request WireRequest
	if err := json.Unmarshal([]byte(`{"tool":"web_search","args":{"query":"go"}}`), &request); err != nil {
		t.Fatalf("unmarshal object request: %v", err)
	}
	if request.Args["query"] != "go" {
		t.Fatalf("object args = %#v", request.Args)
	}
	if err := json.Unmarshal([]byte(`{"tool":"web_search","args":null}`), &request); err != nil {
		t.Fatalf("unmarshal null request: %v", err)
	}
	if request.Args != nil {
		t.Fatalf("null args = %#v", request.Args)
	}
}

func TestServiceDispatchReturnsProviderMetadata(t *testing.T) {
	service := &Service{search: stubSearchProvider{results: []SearchResult{{Title: "Go", URL: "https://go.dev"}}}}
	result, meta, err := service.Dispatch(context.Background(), WireSearchTool, map[string]any{"query": "go", "max_results": float64(1), "include_highlights": false})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if result.Provider != "stub" || !reflect.DeepEqual(meta, map[string]any{"provider": "stub", "result_count": 1, "requested_max_results": 1}) {
		t.Fatalf("result/meta = %+v / %#v", result, meta)
	}
}

func TestValidateSearchSchema(t *testing.T) {
	if err := ValidateSearchSchema(); err != nil {
		t.Fatal(err)
	}
}
