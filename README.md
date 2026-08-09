# webfetch

Fetch web pages as clean Markdown, search the web, or expose both capabilities
to MCP clients from one standalone CLI.

## Quick start

Install the latest release and fetch a page:

```bash
go install github.com/dotcommander/webfetch/cmd/webfetch@latest
webfetch https://example.com
```

Jina Reader can work without a key, subject to its service limits. Search needs
a provider key:

```bash
export BRAVE_API_KEY=...
webfetch search "golang error handling best practices"
```

## Command overview

| Goal | Command |
| --- | --- |
| Fetch readable Markdown | `webfetch https://example.com` |
| Extract locally with Defuddle | `webfetch -reader defuddle https://example.com/article` |
| Search the web | `webfetch search "query"` |
| Run the full MCP server over stdio | `webfetch --mcp` |
| Run the compact MCP router | `webfetch --mcp --compact` |
| Run MCP over Streamable HTTP | `webfetch --mcp-http --listen 127.0.0.1:8787` |
| Print the search tool schema | `webfetch --schema` |

## Usage examples

```bash
webfetch -reader defuddle https://example.com/article
webfetch -reader defuddle --render always https://example.com/app
webfetch -raw https://api.example.com/data.json
webfetch -json https://example.com
webfetch -max-lines 200 -max-bytes 50000 https://example.com/article
webfetch search "golang error handling best practices"
webfetch search -limit 10 -json "rust vs go"
printf '%s\n' '{"tool":"web_search","args":{"query":"Go 1.26"},"request_id":"req-1"}' | webfetch
webfetch --mcp
webfetch --mcp --compact
webfetch --schema
```

## Behavior

- Normal URL fetches use the Jina Reader endpoint and print Markdown.
- `-reader defuddle` fetches HTML directly, extracts readable content locally,
  and prints Markdown with metadata available in JSON mode.
- `-reader auto` tries local Defuddle first and falls back to Jina Reader. The
  default remains Jina, so fallback behavior is opt-in.
- `--render never|auto|always` optionally runs a local Chrome or Chromium page
  render before Defuddle extraction. `never` is the default and never launches
  a browser. `auto` escalates likely JavaScript application shells and keeps a
  static Defuddle result with a warning when rendering fails. `always` makes a
  rendered Defuddle extraction a hard requirement.
- `--render-wait load|networkidle` selects the browser wait strategy. It is only
  valid when rendering is enabled. Rendered JSON and MCP results add `rendered`
  and optional `warnings` fields; human output remains content-only Markdown.
- `-cache-ttl` enables an opt-in cache for successful clean-reader results. A
  zero value disables caching, and `-cache-dir` selects its directory. When
  omitted, the cache uses `$XDG_CACHE_HOME/webfetch/urls` or
  `~/.cache/webfetch/urls`.
- `-raw` fetches the URL directly and prints the response body.
- `-json` emits a machine-readable response envelope.
- `search` keeps Brave as the default provider. Set `WEBFETCH_SEARCH_PROVIDER=exa`
  or pass `-provider exa` to use Exa with `EXA_API_KEY`.
- Search requests accept provider-neutral filters such as category, hostname
  filters, publication date, and Exa highlights. Brave translates hostname
  filters into `site:` query clauses; category and publication-date filters
  require Exa. Results are trimmed, canonicalized for deduplication, and
  limited after duplicates are removed.
- A one-shot JSON search protocol is available through `--protocol`, or
  automatically when a request is piped to `webfetch` with no normal arguments.
  Zero-argument piped startup detects an MCP JSON-RPC request first, allowing
  hosts that accept only an executable path to launch `webfetch` directly.
  Double-encoded `args` and `request_id` are supported. Protocol failures stay
  on stdout as JSON and return a non-zero exit status without diagnostic stderr.
- `--schema` prints and validates the OpenAI-compatible `web_search` schema.
- `--mcp` runs a newline-delimited JSON-RPC MCP server over stdin/stdout. It
  exposes `web_search` and `web_fetch` through the current MCP `2026-07-28`
  SDK, while bridging legacy `initialize` clients such as Jcode to the same
  handlers. Current clients use per-request metadata and `server/discover`.
  Closing stdin exits cleanly; `SIGINT` or `SIGTERM` also stops the server
  promptly even when its parent keeps stdin open.
- `--mcp --compact` (or `--mcp-compact`) exposes one `webfetch` router tool with
  explicit `fetch` and `search` operations. Its JSON Schema 2020-12 definition
  uses `oneOf`, `$defs`, and `$ref` to keep the tool surface compact. The full
  two-tool mode remains the default for client compatibility.
- `webfetch mcp list`, `inspect`, and `call` inspect remote Streamable HTTP
  endpoints or explicit local stdio commands. Calls support repeatable `-a` /
  `--argument` pairs, JSON value coercion, output budgets, and structured tool
  errors without putting diagnostics on protocol stdout.
- The MCP server uses `github.com/voocel/mcp-sdk-go v1.3.0`, a current-only
  SDK implementing the MCP 2.0-era stateless wire contract and Tasks extension.
  A small stdio compatibility bridge accepts the legacy `2024-11-05`
  initialize flow used by Jcode and translates subsequent tool requests.
- See [docs/mcp.md](docs/mcp.md) for the protocol contract, tool schemas,
  compatibility boundary, provider configuration, safety behavior, and checks.
- MCP configuration may launch `webfetch --mcp --compact` to minimize tool
  definition exposure, or `webfetch --mcp` for the full compatibility surface.
  Hosts whose MCP option accepts only an executable path may use `webfetch` with
  no arguments; the first JSON-RPC request selects MCP mode automatically.
  Provider credentials are read from the same environment variables as the CLI.
- `-max-bytes` and `-max-lines` apply an opt-in rendering budget after fetching;
  zero means unlimited and the default output remains complete.
- MCP `web_fetch` accepts the same `max_bytes` and `max_lines` output controls
  and returns truncation metadata when either limit is reached. These limits
  project the fetched document and do not raise the bounded network body cap.
- `search` uses the Brave Web Search API.
- Exa search uses `EXA_API_KEY`, which is only required when the Exa
  provider is explicitly selected.
- HTTP requests have timeouts, bounded response bodies, redirect validation, and
  retry handling for throttling and transient server failures.
- Jina Reader HTTP-200 provider error signals are classified instead of being
  emitted as successful documents, and explicit Jina `Title:` metadata is retained.

## Configuration

Set the provider keys when required:

```bash
export JINA_API_KEY=...
export BRAVE_API_KEY=...
export EXA_API_KEY=...
```

Jina Reader may work without an API key subject to its current service limits.
Brave search requires `BRAVE_API_KEY`.

The Defuddle reader is static-HTML only unless `--render` or the MCP `render`
field is enabled. Browser rendering is opt-in and requires a locally installed
Chrome or Chromium. Configure its executable with `WEBFETCH_CHROME_PATH` when
automatic discovery is not sufficient.

Browser rendering uses a loopback-only proxy backed by the same URL validation,
DNS rebinding checks, private-network policy, redirect limit, and bounded
request/network budgets as static fetching. It does not accept caller-provided
network or timeout budgets through MCP.

Optional render limits are configured through process environment variables:

```text
WEBFETCH_RENDER_TIMEOUT=30s
WEBFETCH_RENDER_MAX_CONCURRENCY=2
WEBFETCH_RENDER_MAX_REQUESTS=100
WEBFETCH_RENDER_MAX_NETWORK_BYTES=33554432
WEBFETCH_RENDER_MAX_HTML_BYTES=8388608
WEBFETCH_CHROME_PATH=/Applications/Google Chrome.app/Contents/MacOS/Google Chrome
```

By default, local, loopback, private, link-local, and unspecified addresses are
rejected to reduce SSRF risk. This policy is enforced for redirects as well.

JSON errors include a stable `error_code` and an actionable `suggestion` when
available. Human-readable errors keep the normal stderr format.

When an output budget truncates content, JSON includes `truncated`,
`truncated_by`, total/output byte and line counts, and the selected limits.
Human output includes a truncation marker. Output budgets do not change the
network response body limit or the default full-content behavior.

## Build from source

The module targets Go 1.26.4. Use the maintained `just` recipes for local
development:

```bash
just build
just test
go vet ./...
```

Without `just`, the equivalent basic build and test commands are:

```bash
go build -o webfetch ./cmd/webfetch/
go test ./...
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

`just test` adds the race detector. The binary also supports `--version` and
`--help`.

### MCP client configuration

```json
{
  "mcpServers": {
    "webfetch": {
      "command": "webfetch",
      "args": ["--mcp"],
      "env": {
        "BRAVE_API_KEY": "...",
        "EXA_API_KEY": "...",
        "JINA_API_KEY": "..."
      }
    }
  }
}
```

The binary also exposes the current MCP protocol over Streamable HTTP. It binds
to loopback by default:

```bash
webfetch --mcp-http --listen 127.0.0.1:8787
```

Non-loopback binds require `WEBFETCH_MCP_BEARER_TOKEN`:

```bash
WEBFETCH_MCP_BEARER_TOKEN='change-me' webfetch --mcp-http --listen 0.0.0.0:8787
```

Use `--allow-origin` to allow a browser origin. The HTTP mode speaks the current
`2026-07-28` protocol only. Legacy `initialize` compatibility remains on stdio.

The installed binary can inspect either HTTP endpoints or explicit MCP
subprocesses:

```bash
webfetch mcp list http://127.0.0.1:8787
webfetch mcp inspect http://127.0.0.1:8787 web_fetch
webfetch mcp call http://127.0.0.1:8787 web_fetch \
  -a url https://example.com -a format headings -a max_lines 20
webfetch mcp list --command webfetch --arg --mcp --arg --compact
webfetch mcp inspect --command webfetch --arg --mcp web_fetch
webfetch mcp call --command webfetch --arg --mcp web_fetch \
  -a url http://127.0.0.1:1/blocked
```

HTTP explorer calls use `WEBFETCH_MCP_BEARER_TOKEN` or an explicit `--token`.
Subprocess mode passes explicit `--arg` values without a shell. Call arguments
can use repeatable `-a` or `--argument` pairs. Values that are valid JSON are
decoded as numbers, booleans, arrays, objects, or null; other values remain
strings. `--args` remains available for a complete JSON object, and later
argument pairs override matching keys. Successful commands write JSON to
stdout. Validation and transport errors exit nonzero, while tool failures are
reported as JSON with `isError: true`.

Set `WEBFETCH_MCP_LOG_LEVEL=error|info|debug` for opt-in stderr diagnostics.
The default is `off`, and protocol stdout remains JSON-RPC-only.

## License

Webfetch is available under the [MIT License](LICENSE).
