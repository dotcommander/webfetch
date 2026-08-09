package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dotcommander/webfetch/internal/webfetch"
	mcpclient "github.com/voocel/mcp-sdk-go/client"
	"github.com/voocel/mcp-sdk-go/protocol"
	"github.com/voocel/mcp-sdk-go/transport/stdio"
	"github.com/voocel/mcp-sdk-go/transport/streamhttp"
)

const mcpExplorerDefaultTimeout = 30 * time.Second

const mcpExplorerHelp = `Usage:
  webfetch mcp list <endpoint> [options]
  webfetch mcp inspect <endpoint> <tool> [options]
  webfetch mcp call <endpoint> <tool> [-a|--argument <name> <value>] [options]

Remote endpoints use Streamable HTTP. Use --command with repeated --arg values
to inspect a local stdio command:
  webfetch mcp list --command webfetch --arg --mcp

Options:
  -a, --argument <name> <value>  set one tool argument; repeat as needed
      --args <json-object>      merge a JSON argument object
      --command <path>          launch a local stdio command
      --arg <value>              pass one argument to --command
      --token <value>            HTTP bearer token
      --timeout <duration>       HTTP request timeout (default 30s)
      --help                    show this help
`

type mcpExplorerOptions struct {
	Endpoint    string
	Command     string
	CommandArgs []string
	Token       string
	Tool        string
	Arguments   map[string]any
	Timeout     time.Duration
}

func runMCPExplorer(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		_, err := io.WriteString(stdout, mcpExplorerHelp)
		return err
	}
	operation := strings.ToLower(strings.TrimSpace(args[0]))
	options, err := parseMCPExplorerOptions(operation, args[1:])
	if err != nil {
		return err
	}
	client, closeClient, err := newMCPExplorerClient(options)
	if err != nil {
		return err
	}
	defer closeClient()

	switch operation {
	case "list":
		return runMCPExplorerList(ctx, client, stdout)
	case "inspect":
		return runMCPExplorerInspect(ctx, client, options.Tool, stdout)
	case "call":
		return runMCPExplorerCall(ctx, client, options.Tool, options.Arguments, stdout)
	default:
		return fmt.Errorf("unknown MCP explorer operation %q", operation)
	}
}

func parseMCPExplorerOptions(operation string, args []string) (mcpExplorerOptions, error) {
	switch operation {
	case "list", "inspect", "call":
	default:
		return mcpExplorerOptions{}, fmt.Errorf("unknown MCP explorer operation %q", operation)
	}
	options := mcpExplorerOptions{
		Token:   strings.TrimSpace(os.Getenv(mcpBearerTokenEnv)),
		Timeout: mcpExplorerDefaultTimeout,
	}
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--token":
			value, next, err := explorerValue(args, i, "--token")
			if err != nil {
				return mcpExplorerOptions{}, err
			}
			options.Token = value
			i = next
		case "--args":
			if operation != "call" {
				return mcpExplorerOptions{}, fmt.Errorf("--args is only valid for call")
			}
			value, next, err := explorerValue(args, i, "--args")
			if err != nil {
				return mcpExplorerOptions{}, err
			}
			var parsed map[string]any
			if err := json.Unmarshal([]byte(value), &parsed); err != nil || parsed == nil {
				return mcpExplorerOptions{}, fmt.Errorf("--args must be a JSON object")
			}
			if options.Arguments == nil {
				options.Arguments = make(map[string]any, len(parsed))
			}
			for name, argument := range parsed {
				options.Arguments[name] = argument
			}
			i = next
		case "-a", "--argument":
			if operation != "call" {
				return mcpExplorerOptions{}, fmt.Errorf("%s is only valid for call", args[i])
			}
			name, next, err := explorerValue(args, i, args[i])
			if err != nil {
				return mcpExplorerOptions{}, err
			}
			value, final, err := explorerValue(args, next, args[i])
			if err != nil {
				return mcpExplorerOptions{}, err
			}
			name = strings.TrimSpace(name)
			if name == "" {
				return mcpExplorerOptions{}, fmt.Errorf("%s requires a non-empty argument name", args[i])
			}
			if options.Arguments == nil {
				options.Arguments = make(map[string]any)
			}
			options.Arguments[name] = parseMCPExplorerArgumentValue(value)
			i = final
		case "--command":
			value, next, err := explorerValue(args, i, "--command")
			if err != nil {
				return mcpExplorerOptions{}, err
			}
			options.Command = value
			i = next
		case "--arg":
			value, next, err := explorerValue(args, i, "--arg")
			if err != nil {
				return mcpExplorerOptions{}, err
			}
			options.CommandArgs = append(options.CommandArgs, value)
			i = next
		case "--timeout":
			value, next, err := explorerValue(args, i, "--timeout")
			if err != nil {
				return mcpExplorerOptions{}, err
			}
			timeout, err := time.ParseDuration(value)
			if err != nil || timeout <= 0 {
				return mcpExplorerOptions{}, fmt.Errorf("--timeout must be a positive duration")
			}
			options.Timeout = timeout
			i = next
		default:
			if strings.HasPrefix(args[i], "-") {
				return mcpExplorerOptions{}, fmt.Errorf("unknown MCP explorer option %q", args[i])
			}
			positional = append(positional, args[i])
		}
	}
	if options.Command != "" {
		want := 0
		if operation != "list" {
			want = 1
		}
		if len(positional) != want {
			if operation == "list" {
				return mcpExplorerOptions{}, fmt.Errorf("subprocess MCP list does not accept an endpoint")
			}
			return mcpExplorerOptions{}, fmt.Errorf("subprocess MCP %s requires exactly one tool name", operation)
		}
		if operation != "list" {
			options.Tool = positional[0]
		}
	} else {
		want := 1
		if operation != "list" {
			want = 2
		}
		if len(positional) != want {
			if len(positional) == 0 {
				return mcpExplorerOptions{}, fmt.Errorf("%s requires an HTTP(S) endpoint", operation)
			}
			if operation == "list" {
				return mcpExplorerOptions{}, fmt.Errorf("list expects exactly one HTTP(S) endpoint")
			}
			return mcpExplorerOptions{}, fmt.Errorf("%s expects exactly one endpoint and one tool name", operation)
		}
		options.Endpoint = positional[0]
		if len(positional) > 1 {
			options.Tool = positional[1]
		}
	}
	if operation != "list" && options.Tool == "" {
		return mcpExplorerOptions{}, fmt.Errorf("%s requires a tool name", operation)
	}
	if operation == "call" && options.Arguments == nil {
		options.Arguments = make(map[string]any)
	}
	return options, nil
}

func explorerValue(args []string, index int, flag string) (string, int, error) {
	if index+1 >= len(args) {
		return "", index, fmt.Errorf("%s requires a value", flag)
	}
	return args[index+1], index + 1, nil
}

func parseMCPExplorerArgumentValue(value string) any {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return value
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return value
	}
	return parsed
}

func newMCPExplorerClient(options mcpExplorerOptions) (*mcpclient.Client, func(), error) {
	if options.Command != "" {
		command := exec.Command(options.Command, options.CommandArgs...)
		command.Env = os.Environ()
		transport, err := stdio.NewCommand(command, &stdio.CommandOptions{Stderr: io.Discard})
		if err != nil {
			return nil, func() {}, fmt.Errorf("start MCP command: %w", err)
		}
		client := mcpclient.New(transport, &mcpclient.Options{
			Info: &protocol.Implementation{Name: "webfetch-mcp-explorer", Version: webfetch.ResolveVersion(version)},
		})
		return client, func() { _ = client.Close() }, nil
	}

	parsed, err := url.Parse(options.Endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, func() {}, fmt.Errorf("MCP explorer endpoint must be an HTTP(S) URL")
	}
	transport := streamhttp.New(options.Endpoint, &streamhttp.TransportOptions{
		HTTPClient: &http.Client{
			Timeout:   options.Timeout,
			Transport: mcpBearerRoundTripper{base: http.DefaultTransport, token: options.Token},
		},
		MaxRetries: 0,
	})
	client := mcpclient.New(transport, &mcpclient.Options{
		Info: &protocol.Implementation{Name: "webfetch-mcp-explorer", Version: webfetch.ResolveVersion(version)},
	})
	return client, func() { _ = client.Close() }, nil
}

type mcpBearerRoundTripper struct {
	base  http.RoundTripper
	token string
}

func (t mcpBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.TrimSpace(t.token) == "" {
		return t.base.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func runMCPExplorerList(ctx context.Context, client *mcpclient.Client, stdout io.Writer) error {
	discovery, err := client.Discover(ctx)
	if err != nil {
		return fmt.Errorf("discover MCP server: %w", err)
	}
	tools, err := listAllMCPTools(ctx, client)
	if err != nil {
		return err
	}
	return writeMCPExplorerJSON(stdout, map[string]any{
		"server":    discovery.Meta.ServerInfo,
		"discovery": discovery,
		"tools":     tools,
	})
}

func runMCPExplorerInspect(ctx context.Context, client *mcpclient.Client, name string, stdout io.Writer) error {
	tools, err := listAllMCPTools(ctx, client)
	if err != nil {
		return err
	}
	for _, tool := range tools {
		if tool.Name == name {
			return writeMCPExplorerJSON(stdout, tool)
		}
	}
	return fmt.Errorf("MCP tool %q was not found", name)
}

func runMCPExplorerCall(ctx context.Context, client *mcpclient.Client, name string, arguments map[string]any, stdout io.Writer) error {
	result, err := client.CallTool(ctx, &protocol.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return fmt.Errorf("call MCP tool %q: %w", name, err)
	}
	return writeMCPExplorerJSON(stdout, map[string]any{
		"resultType":        result.ResultType(),
		"content":           result.Content,
		"structuredContent": result.StructuredContent,
		"isError":           result.IsError,
	})
}

func listAllMCPTools(ctx context.Context, client *mcpclient.Client) ([]*protocol.Tool, error) {
	var tools []*protocol.Tool
	cursor := ""
	for {
		result, err := client.ListTools(ctx, cursor)
		if err != nil {
			return nil, fmt.Errorf("list MCP tools: %w", err)
		}
		tools = append(tools, result.Tools...)
		if result.NextCursor == "" {
			return tools, nil
		}
		cursor = result.NextCursor
	}
}

func writeMCPExplorerJSON(stdout io.Writer, value any) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
