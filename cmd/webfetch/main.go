package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/dotcommander/webfetch/internal/webfetch"
)

var version = "dev"

const helpText = `Usage:
  webfetch [flags] <url>           fetch a URL as clean Markdown
	webfetch search [flags] <query>  search the web
	webfetch --protocol              read one JSON search request from stdin
	webfetch --mcp                   run the full MCP tool surface over stdio
	webfetch --mcp --compact         run one compact MCP router tool over stdio
	webfetch --mcp-http              run the current MCP surface over Streamable HTTP
	webfetch mcp list <endpoint>     inspect an MCP endpoint
	webfetch mcp call <endpoint> <tool> -a name value

Flags (fetch):
  -raw, -json                      fetch directly or emit JSON
  -reader string                   reader for HTML pages (jina, defuddle, or auto)
  --render string                  browser rendering policy (never, auto, or always)
  --render-wait string             wait strategy when rendering (load or networkidle)
  -max-bytes int                   limit rendered content bytes (default unlimited)
  -max-lines int                   limit rendered content lines (default unlimited)
  -cache-ttl string                cache successful reader results for a duration (default disabled)
  -cache-dir string                directory for cached reader results


Flags (search):
  -limit int                       maximum results (default 5)
  -provider string                 search provider (brave or exa)
  -category string                 provider category
  -include-domain string           hostname filter; may be repeated
  -start-published-date string     publication lower bound
  -include-highlights              request provider highlights
  -highlight-sentences int         number of highlight sentences
  -json                            emit JSON

Global:
  -v, --version                    print version
  -h, --help                       print this help
	--schema                         print the OpenAI-compatible web_search schema
	--protocol                       read one JSON request from stdin
	--mcp                            run the full MCP tool surface over stdio
	--mcp --compact                  run one compact MCP router tool over stdio
	--mcp-compact                    shorthand for --mcp --compact
	--mcp-http                      run the current MCP surface over Streamable HTTP

Examples:
	webfetch https://example.com
	webfetch -reader defuddle https://example.com/article
	webfetch -reader defuddle --render always https://example.com/app
	webfetch -raw https://api.example.com/data.json
  webfetch -json https://example.com
  webfetch search "golang error handling best practices"
  webfetch search -limit 10 -json "rust vs go"
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "--mcp" || args[0] == "-mcp" || args[0] == "--mcp-compact" || args[0] == "-mcp-compact") {
		mcpArgs := args[1:]
		if args[0] == "--mcp-compact" || args[0] == "-mcp-compact" {
			mcpArgs = append([]string{"--compact"}, mcpArgs...)
		}
		if err := runMCP(ctx, mcpArgs); err != nil {
			fmt.Fprintf(os.Stderr, "webfetch MCP: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if len(args) > 0 && (args[0] == "--mcp-http" || args[0] == "-mcp-http") {
		if err := runMCPHTTP(ctx, args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "webfetch MCP HTTP: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if shouldRunProtocol(args, os.Stdin) {
		runStdin := runProtocol
		if len(args) == 0 {
			runStdin = runPipedInput
		}
		if err := runStdin(ctx, os.Stdin, os.Stdout); err != nil {
			var protocolErr *protocolError
			if !errors.As(err, &protocolErr) {
				fmt.Fprintf(os.Stderr, "webfetch: %v\n", err)
			}
			os.Exit(1)
		}
		return
	}
	if err := run(ctx, args, os.Stdout, os.Stderr); err != nil {
		var cliErr *cliError
		if errors.As(err, &cliErr) && cliErr.JSON {
			if writeErr := renderJSONError(os.Stdout, cliErr.Err); writeErr != nil {
				fmt.Fprintf(os.Stderr, "webfetch: write JSON error: %v\n", writeErr)
			}
		} else {
			fmt.Fprintf(os.Stderr, "webfetch: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return runWithService(ctx, args, stdout, stderr, nil)
}

func runWithService(ctx context.Context, args []string, stdout, stderr io.Writer, service *webfetch.Service) error {
	args = normalizeFlags(args)
	if len(args) == 0 {
		_, err := io.WriteString(stdout, helpText)
		return err
	}
	if args[0] == "--version" || args[0] == "-v" {
		_, err := fmt.Fprintln(stdout, webfetch.ResolveVersion(version))
		return err
	}
	if args[0] == "--schema" {
		if len(args) > 1 {
			return errors.New("--schema does not accept positional arguments")
		}
		if err := webfetch.ValidateSearchSchema(); err != nil {
			return err
		}
		_, err := fmt.Fprintln(stdout, webfetch.SearchSchema)
		return err
	}
	if args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(stdout, helpText)
		return err
	}
	if args[0] == "completion" {
		return errors.New("unknown command: completion")
	}
	if args[0] == "search" {
		return runSearch(ctx, args[1:], stdout, stderr, service)
	}
	if args[0] == "mcp" {
		return runMCPExplorer(ctx, args[1:], stdout, stderr)
	}
	return runFetch(ctx, args, stdout, stderr, service)
}

func runFetch(ctx context.Context, args []string, stdout, stderr io.Writer, service *webfetch.Service) error {
	var command fetchCmd
	parser, err := newParser("webfetch", &command, stdout, stderr)
	if err != nil {
		return err
	}
	parsed, err := parseCLI(parser, args)
	if err != nil {
		return err
	}
	if parsed == nil {
		return nil
	}
	cacheTTL, err := parseCacheTTL(command.CacheTTL)
	if err != nil {
		return &cliError{Err: webfetch.NewCodedError(err, webfetch.ErrorCodeInvalidArgument, "use a non-negative duration such as 24h or 0 to disable caching"), JSON: command.JSON}
	}
	if service == nil {
		service = configuredService(webfetch.Config{URLCacheTTL: cacheTTL, URLCacheDir: command.CacheDir})
		ownedService := true
		return runAndCloseService(service, ownedService, func() error {
			return parsed.Run(&commandDeps{ctx: ctx, service: service, stdout: stdout})
		})
	}
	return parsed.Run(&commandDeps{ctx: ctx, service: service, stdout: stdout})
}

func runSearch(ctx context.Context, args []string, stdout, stderr io.Writer, service *webfetch.Service) error {
	var command searchCmd
	parser, err := newParser("webfetch search", &command, stdout, stderr)
	if err != nil {
		return err
	}
	parsed, err := parseCLI(parser, args)
	if err != nil {
		return err
	}
	if parsed == nil {
		return nil
	}
	if service == nil {
		service = configuredService(webfetch.Config{SearchProvider: command.Provider})
		ownedService := true
		return runAndCloseService(service, ownedService, func() error {
			return parsed.Run(&commandDeps{ctx: ctx, service: service, stdout: stdout})
		})
	}
	return parsed.Run(&commandDeps{ctx: ctx, service: service, stdout: stdout})
}

func runAndCloseService(service *webfetch.Service, owned bool, run func() error) error {
	if !owned {
		return run()
	}
	runErr := run()
	closeErr := closeService(service)
	return errors.Join(runErr, closeErr)
}

func closeService(service *webfetch.Service) error {
	if service == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return service.Close(ctx)
}

func configuredService(overrides webfetch.Config) *webfetch.Service {
	jinaAPIKey, _ := os.LookupEnv("JINA_API_KEY")
	braveAPIKey, _ := os.LookupEnv("BRAVE_API_KEY")
	exaAPIKey, _ := os.LookupEnv("EXA_API_KEY")
	if strings.TrimSpace(overrides.SearchProvider) == "" {
		overrides.SearchProvider = strings.TrimSpace(os.Getenv("WEBFETCH_SEARCH_PROVIDER"))
	}
	overrides.JinaAPIKey = jinaAPIKey
	overrides.BraveAPIKey = braveAPIKey
	overrides.ExaAPIKey = exaAPIKey
	if overrides.RenderTimeout <= 0 {
		overrides.RenderTimeout = positiveDurationEnv("WEBFETCH_RENDER_TIMEOUT")
	}
	if overrides.RenderMaxConcurrency <= 0 {
		overrides.RenderMaxConcurrency = positiveIntEnv("WEBFETCH_RENDER_MAX_CONCURRENCY")
	}
	if overrides.RenderMaxRequests <= 0 {
		overrides.RenderMaxRequests = positiveIntEnv("WEBFETCH_RENDER_MAX_REQUESTS")
	}
	if overrides.RenderMaxNetworkBytes <= 0 {
		overrides.RenderMaxNetworkBytes = positiveInt64Env("WEBFETCH_RENDER_MAX_NETWORK_BYTES")
	}
	if overrides.RenderMaxHTMLBytes <= 0 {
		overrides.RenderMaxHTMLBytes = positiveInt64Env("WEBFETCH_RENDER_MAX_HTML_BYTES")
	}
	if strings.TrimSpace(overrides.ChromePath) == "" {
		overrides.ChromePath = strings.TrimSpace(os.Getenv("WEBFETCH_CHROME_PATH"))
	}
	return webfetch.NewService(overrides)
}

func positiveDurationEnv(name string) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0
	}
	return duration
}

func positiveIntEnv(name string) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func positiveInt64Env(name string) int64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func newParser(name string, target any, stdout, stderr io.Writer) (*kong.Kong, error) {
	return kong.New(target,
		kong.Name(name),
		kong.Writers(stdout, stderr),
		kong.Exit(func(code int) { panic(parserExit(code)) }),
		kong.ConfigureHelp(kong.HelpOptions{
			Compact:   true,
			Tree:      true,
			Summary:   true,
			FlagsLast: true,
		}),
	)
}

func parseCLI(parser *kong.Kong, args []string) (parsed *kong.Context, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if _, ok := recovered.(parserExit); ok {
				parsed = nil
				err = nil
				return
			}
			panic(recovered)
		}
	}()
	return parser.Parse(args)
}

func normalizeFlags(args []string) []string {
	normalized := make([]string, 0, len(args))
	for _, arg := range args {
		switch {
		case arg == "-raw":
			normalized = append(normalized, "--raw")
		case arg == "-json":
			normalized = append(normalized, "--json")
		case arg == "-reader":
			normalized = append(normalized, "--reader")
		case strings.HasPrefix(arg, "-reader="):
			normalized = append(normalized, "--"+strings.TrimPrefix(arg, "-"))
		case arg == "-render":
			normalized = append(normalized, "--render")
		case strings.HasPrefix(arg, "-render="):
			normalized = append(normalized, "--"+strings.TrimPrefix(arg, "-"))
		case arg == "-render-wait":
			normalized = append(normalized, "--render-wait")
		case strings.HasPrefix(arg, "-render-wait="):
			normalized = append(normalized, "--"+strings.TrimPrefix(arg, "-"))
		case arg == "-limit":
			normalized = append(normalized, "--limit")
		case strings.HasPrefix(arg, "-limit="):
			normalized = append(normalized, "--"+strings.TrimPrefix(arg, "-"))
		case arg == "-provider":
			normalized = append(normalized, "--provider")
		case strings.HasPrefix(arg, "-provider="):
			normalized = append(normalized, "--"+strings.TrimPrefix(arg, "-"))
		case arg == "-category":
			normalized = append(normalized, "--category")
		case strings.HasPrefix(arg, "-category="):
			normalized = append(normalized, "--"+strings.TrimPrefix(arg, "-"))
		case arg == "-include-domain":
			normalized = append(normalized, "--include-domain")
		case strings.HasPrefix(arg, "-include-domain="):
			normalized = append(normalized, "--"+strings.TrimPrefix(arg, "-"))
		case arg == "-start-published-date":
			normalized = append(normalized, "--start-published-date")
		case strings.HasPrefix(arg, "-start-published-date="):
			normalized = append(normalized, "--"+strings.TrimPrefix(arg, "-"))
		case arg == "-include-highlights":
			normalized = append(normalized, "--include-highlights")
		case arg == "-highlight-sentences":
			normalized = append(normalized, "--highlight-sentences")
		case strings.HasPrefix(arg, "-highlight-sentences="):
			normalized = append(normalized, "--"+strings.TrimPrefix(arg, "-"))
		case arg == "-max-bytes":
			normalized = append(normalized, "--max-bytes")
		case strings.HasPrefix(arg, "-max-bytes="):
			normalized = append(normalized, "--"+strings.TrimPrefix(arg, "-"))
		case arg == "-max-lines":
			normalized = append(normalized, "--max-lines")
		case strings.HasPrefix(arg, "-max-lines="):
			normalized = append(normalized, "--"+strings.TrimPrefix(arg, "-"))
		case arg == "-cache-ttl":
			normalized = append(normalized, "--cache-ttl")
		case strings.HasPrefix(arg, "-cache-ttl="):
			normalized = append(normalized, "--"+strings.TrimPrefix(arg, "-"))
		case arg == "-cache-dir":
			normalized = append(normalized, "--cache-dir")
		case strings.HasPrefix(arg, "-cache-dir="):
			normalized = append(normalized, "--"+strings.TrimPrefix(arg, "-"))
		default:
			normalized = append(normalized, arg)
		}
	}
	return normalized
}

type protocolError struct{ Err error }

func (e *protocolError) Error() string { return e.Err.Error() }
func (e *protocolError) Unwrap() error { return e.Err }

func shouldRunProtocol(args []string, in *os.File) bool {
	if len(args) > 0 && (args[0] == "--protocol" || args[0] == "-protocol") {
		return true
	}
	return len(args) == 0 && !stdinIsTerminal(in)
}

func stdinIsTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func runProtocol(ctx context.Context, in io.Reader, out io.Writer) error {
	service := configuredService(webfetch.Config{})
	return runAndCloseService(service, true, func() error {
		return runProtocolWithService(ctx, in, out, service)
	})
}

func runProtocolWithService(ctx context.Context, in io.Reader, out io.Writer, service *webfetch.Service) error {
	var req webfetch.WireRequest
	decoder := json.NewDecoder(in)
	if err := decoder.Decode(&req); err != nil {
		code := webfetch.ErrorCodeInvalidJSON
		suggestion := "send one valid JSON request object on stdin"
		if errors.Is(err, io.EOF) {
			code = webfetch.ErrorCodeNoInput
			suggestion = `send {"tool":"web_search","args":{"query":"..."}}`
			err = errors.New("no input: pipe a JSON request to stdin")
		} else {
			err = fmt.Errorf("invalid JSON: %w", err)
		}
		coded := webfetch.NewCodedError(err, code, suggestion)
		if writeErr := writeProtocolResponse(out, webfetch.WireResponse{Error: coded.Error(), ErrorCode: code, Suggestion: suggestion}); writeErr != nil {
			return writeErr
		}
		return &protocolError{Err: coded}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		const suggestion = "send one valid JSON request object on stdin"
		decodeErr := errors.New("invalid JSON: stdin must contain exactly one JSON value")
		if err != nil {
			decodeErr = fmt.Errorf("invalid JSON: %w", err)
		}
		coded := webfetch.NewCodedError(decodeErr, webfetch.ErrorCodeInvalidJSON, suggestion)
		if writeErr := writeProtocolResponse(out, webfetch.WireResponse{Error: coded.Error(), ErrorCode: webfetch.ErrorCodeInvalidJSON, Suggestion: suggestion}); writeErr != nil {
			return writeErr
		}
		return &protocolError{Err: coded}
	}

	if service == nil {
		service = configuredService(webfetch.Config{})
	}
	result, meta, err := service.Dispatch(ctx, req.Tool, req.Args)
	if err != nil {
		response := protocolErrorResponse(err, req.RequestID)
		if writeErr := writeProtocolResponse(out, response); writeErr != nil {
			return writeErr
		}
		return &protocolError{Err: err}
	}
	if err := writeProtocolResponse(out, webfetch.WireResponse{
		OK:        true,
		Result:    result,
		Meta:      meta,
		RequestID: req.RequestID,
	}); err != nil {
		return err
	}
	return nil
}

func protocolErrorResponse(err error, requestID string) webfetch.WireResponse {
	response := webfetch.WireResponse{Error: err.Error(), RequestID: requestID}
	var coded *webfetch.CodedError
	if errors.As(err, &coded) {
		response.ErrorCode = coded.Code
		response.Suggestion = coded.Suggestion
	}
	return response
}

func writeProtocolResponse(out io.Writer, response webfetch.WireResponse) error {
	return json.NewEncoder(out).Encode(response)
}
