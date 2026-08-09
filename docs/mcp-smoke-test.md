# MCP stdio and HTTP smoke-test plan

This document is the executable test contract for the `webfetch --mcp` and
`webfetch --mcp-http` integrations. It covers both the current `2026-07-28`
wire contract and the legacy `2024-11-05` handshake used by Jcode. It is
intentionally separate from
the protocol description in
[`mcp.md`](mcp.md): `mcp.md` explains what the server implements, while this
document defines how an installed binary is proven to work.

The plan has five layers:

1. **Installed-binary black box:** exercise newline-delimited JSON-RPC over
   the real `webfetch --mcp` process, including both protocol modes.
2. **Installed-binary HTTP:** launch `webfetch --mcp-http` on loopback and
   exercise current discovery, auth, and shutdown over Streamable HTTP.
3. **Explorer dogfood:** exercise remote HTTP and local stdio `list`, `inspect`,
   and `call` commands against full and compact surfaces, including safe tool
   errors and output-budget arguments.
4. **In-process integration:** exercise successful fetch and search calls with
   local fake providers, without requiring external API keys.
   The optional Chrome integration also exercises a controlled JavaScript page
   through the real renderer and Defuddle path.
5. **Repository gates:** build, tests, race detection, vet, module integrity,
   formatting, and diff checks.

The test run must not require production credentials or make uncontrolled
network requests. Local fake providers are used for successful tool calls. The
installed binary is tested with provider-independent protocol and validation
cases plus the default SSRF policy.

## Contract under test

- Current protocol: MCP `2026-07-28`, called “MCP 2.0” in project shorthand.
- Legacy compatibility: MCP `2024-11-05` `initialize` followed by
  `notifications/initialized`, `tools/list`, and `tools/call`.
- Transport: newline-delimited JSON-RPC over stdin/stdout.
- HTTP transport: one current-protocol POST per request through
  `webfetch --mcp-http`.
- Discovery: `server/discover` with current `_meta`.
- Full mode tools: `web_fetch` and `web_search`.
- Compact mode tool: one `webfetch` router with explicit `fetch` and `search`
  operations.
- Tool calls: `tools/call` with current `_meta` and `resultType: "complete"`.
- Compatibility: current requests require `_meta`; a legacy `initialize`
  handshake is accepted and subsequent tool requests are translated to the
  current SDK contract without creating a session.
- Process behavior: stdout contains only JSON-RPC messages. Diagnostics may go
  to stderr. Closing stdin permits a clean exit. `SIGINT` must also terminate
  promptly while the parent keeps stdin open.
- Default safety: private, loopback, link-local, multicast, and unspecified
  network targets are rejected by the installed CLI.
- HTTP safety: loopback binds work without auth; non-loopback binds require
  `WEBFETCH_MCP_BEARER_TOKEN`; invalid tokens return HTTP 401.
- Fetch projections: `format` accepts `markdown`, `headings`, and `links`, with
  zero-based `start_line` and optional `next_start_line` continuation.
- Render policy: `web_fetch` schemas advertise `render: never|auto|always`,
  `render_wait: load|networkidle`, and additive `rendered`/`warnings` output.
- Explorer CLI: remote HTTP and local stdio `list`, `inspect`, and `call`; full
  and compact schemas; bearer authentication; repeatable `-a` argument pairs;
  structured tool errors; and output-budget forwarding.
- Tool catalog: discovery and `tools/list` advertise `ttlMs: 60000` and
  `cacheScope: public`.
- Diagnostics: `WEBFETCH_MCP_LOG_LEVEL` is off by default and writes only to
  stderr when enabled.

## Preconditions

Run from the repository root with Go installed:

```bash
go version
go install ./cmd/webfetch

BIN_DIR="$(go env GOBIN)"
if [ -z "$BIN_DIR" ]; then BIN_DIR="$(go env GOPATH)/bin"; fi
BIN="$BIN_DIR/webfetch"
test -x "$BIN"
"$BIN" --version
"$BIN" --help >/dev/null
```

The installed binary must come from the current checkout. Do not use a remote
`@latest` install for this test. Provider keys are not required for the
protocol-only cases. The in-process fake-provider tests supply local endpoints
and test credentials through `webfetch.Config`.

## Wire fixtures

The current metadata fragment is included in every current request:

```json
"_meta": {
  "io.modelcontextprotocol/protocolVersion": "2026-07-28",
  "io.modelcontextprotocol/clientInfo": {
    "name": "webfetch-smoke-test",
    "version": "test"
  },
  "io.modelcontextprotocol/clientCapabilities": {}
}
```

Discovery fixture:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "server/discover",
  "params": {
    "_meta": {
      "io.modelcontextprotocol/protocolVersion": "2026-07-28",
      "io.modelcontextprotocol/clientInfo": {
        "name": "webfetch-smoke-test",
        "version": "test"
      },
      "io.modelcontextprotocol/clientCapabilities": {}
    }
  }
}
```

Tool-list fixture:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "tools/list",
  "params": {
    "_meta": {
      "io.modelcontextprotocol/protocolVersion": "2026-07-28",
      "io.modelcontextprotocol/clientInfo": {
        "name": "webfetch-smoke-test",
        "version": "test"
      },
      "io.modelcontextprotocol/clientCapabilities": {}
    }
  }
}
```

Invalid search fixture:

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "tools/call",
  "params": {
    "_meta": {
      "io.modelcontextprotocol/protocolVersion": "2026-07-28",
      "io.modelcontextprotocol/clientInfo": {
        "name": "webfetch-smoke-test",
        "version": "test"
      },
      "io.modelcontextprotocol/clientCapabilities": {}
    },
    "name": "web_search",
    "arguments": {}
  }
}
```

SSRF fixture:

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "tools/call",
  "params": {
    "_meta": {
      "io.modelcontextprotocol/protocolVersion": "2026-07-28",
      "io.modelcontextprotocol/clientInfo": {
        "name": "webfetch-smoke-test",
        "version": "test"
      },
      "io.modelcontextprotocol/clientCapabilities": {}
    },
    "name": "web_fetch",
    "arguments": {
      "url": "http://127.0.0.1:1/blocked"
    }
  }
}
```

The script may compact these fixtures onto one line before sending them. Each
request must remain one JSON value per line.

Compact router fixture:

```json
{
  "jsonrpc": "2.0",
  "id": 10,
  "method": "tools/call",
  "params": {
    "_meta": {
      "io.modelcontextprotocol/protocolVersion": "2026-07-28",
      "io.modelcontextprotocol/clientInfo": {
        "name": "webfetch-smoke-test",
        "version": "test"
      },
      "io.modelcontextprotocol/clientCapabilities": {}
    },
    "name": "webfetch",
    "arguments": {
      "operation": "fetch",
      "input": {
        "url": "https://example.com"
      }
    }
  }
}
```

## Test matrix

### Installed-binary black-box cases

These cases are implemented by `scripts/mcp-smoke-test.py`. A failed required
case fails the command and prints the request, response, process status, and
captured stderr needed to reproduce it.

| ID | Case | Expected evidence |
| --- | --- | --- |
| B-01 | Binary identity | `--version` exits `0`, prints a non-empty version, and `--help` mentions `--mcp`. |
| B-02 | Current discovery | `server/discover` returns one supported version, exactly `2026-07-28`, advertises tools, and returns `resultType: "complete"`. |
| B-03 | Tool listing | `tools/list` returns exactly `web_fetch` and `web_search`; both have object input schemas and structured output schemas. `web_fetch` also advertises the render enums and rendered metadata fields. |
| B-04 | Invalid search arguments | `tools/call` for `web_search` with `{}` returns a JSON-RPC success envelope whose result has `isError: true` and text mentioning `query`. |
| B-05 | Missing current metadata | A `tools/list` request without `_meta` is rejected as a protocol error, not treated as a successful tool result. |
| B-06 | Legacy handshake compatibility | `initialize` returns a `2024-11-05` result with webfetch server info, and the process remains usable. |
| B-07 | Legacy tool listing | After the legacy handshake, a metadata-free `tools/list` returns exactly `web_fetch` and `web_search`. |
| B-08 | Unknown method | An unknown method returns a JSON-RPC method error and does not emit non-JSON data on stdout. |
| B-09 | SSRF policy | A default-policy `web_fetch` call for loopback returns `isError: true` with a private-network or loopback validation message. |
| B-10 | Malformed input | A malformed JSON line returns a parse error response. Any following valid request still receives a valid response. |
| B-11 | JSON-RPC line discipline | Every non-empty stdout line emitted during the run parses as a JSON object with `jsonrpc: "2.0"`. |
| B-12 | Clean EOF | Closing stdin produces exit status `0` within the timeout. |
| B-13 | Stderr discipline | The successful protocol run emits no unexpected stderr diagnostics. |
| B-14 | Executable-only host startup | Launching the binary without arguments auto-detects an MCP `initialize` request and exits cleanly after EOF. |
| B-15 | Signal shutdown | After a successful discovery request, `SIGINT` terminates the process within the timeout while stdin remains open. |

The black-box script does not call the public internet. It deliberately
exercises a rejected local URL instead of attempting a provider call with
missing credentials.

### Installed-binary HTTP cases

These cases run with `--mode http` and use a loopback listener selected by the
smoke script. They do not require provider credentials.

| ID | Case | Expected evidence |
| --- | --- | --- |
| H-01 | HTTP help | `--help` advertises HTTP and explorer modes. |
| H-02 | HTTP discovery | `--mcp-http` starts on loopback and accepts a current discovery POST. |
| H-03 | HTTP headers | Missing or mismatched current MCP headers are rejected by the transport. |
| H-04 | Cache controls | Discovery and tools/list include `ttlMs: 60000` and `cacheScope: public`. |
| H-05 | HTTP auth | A protected non-loopback configuration rejects missing credentials with 401 and accepts the configured bearer token. |
| H-06 | HTTP shutdown | Terminating the process closes the listener without leaving a child process. |

### Compact installed-binary cases

Run these with `--mode compact`. The compact process must expose one tool and
retain the same protocol, safety, and shutdown guarantees.

| ID | Case | Expected evidence |
| --- | --- | --- |
| C-01 | Binary identity | `--version` and `--help` succeed; help mentions compact MCP mode. |
| C-02 | Current discovery | `server/discover` returns only `2026-07-28` and `resultType: "complete"`. |
| C-03 | Compact schema | `tools/list` returns exactly `webfetch`; input schema declares JSON Schema 2020-12, two `oneOf` branches, `$defs`, and operation-specific definitions. |
| C-04 | Invalid operation | A compact call with an unknown operation returns `isError: true` tool content mentioning `operation`. |
| C-05 | SSRF policy | A compact fetch operation targeting loopback returns `isError: true` with a private-network or loopback message. |
| C-06 | Missing current metadata | A request without `_meta` is rejected with `-32602`. |
| C-07 | Legacy handshake compatibility | `initialize` returns the `2024-11-05` compatibility result. |
| C-08 | Legacy tool listing | After the legacy handshake, metadata-free `tools/list` returns only `webfetch`. |
| C-09 | Unknown method | An unknown method is rejected with `-32601`. |
| C-10 | Malformed input recovery | A malformed JSON line returns `-32700`, then a valid request still succeeds. |
| C-11 | JSON-RPC line discipline | Every emitted stdout line is a JSON-RPC 2.0 object. |
| C-12 | Clean EOF | Closing stdin produces exit status `0`. |
| C-13 | Stderr discipline | The compact protocol run emits no unexpected stderr. |

### In-process fake-provider cases

These cases live beside the MCP adapter tests in `cmd/webfetch/mcp_test.go`.
They use the SDK memory transport and `httptest.Server` endpoints.

| ID | Case | Expected evidence |
| --- | --- | --- |
| G-01 | Successful `web_fetch` reader call | A local Defuddle or Jina fixture returns a structured result with the expected URL, status, source, title, and Markdown content. |
| G-02 | Successful `web_fetch` raw call | A local JSON fixture returns `source: "raw"`, status `200`, content type, and the bounded body unchanged. |
| G-03 | Successful Brave `web_search` call | The fake endpoint sees the API token, query, and count; MCP returns normalized provider, result URL, description, and highlight data. |
| G-04 | Cancellation propagation | A slow local provider observes request cancellation and the MCP call returns without waiting for the provider delay. The SDK may surface this as an ended stream because cancelled requests must not emit a late response. |
| G-05 | Tool error/result separation | Invalid arguments remain `isError: true` tool results, while encoding failures remain Go errors from the adapter. |
| G-06 | Compact one-tool listing | The compact in-process server lists only `webfetch`, with two operation branches and `$defs` references. |
| G-07 | Compact fetch routing | `operation: fetch` reaches the existing reader path and returns a wrapped structured result. |
| G-08 | Compact search routing | `operation: search` reaches the existing Brave path and returns a wrapped structured result. |
| G-09 | Compact router errors | Unknown operations remain `isError: true` tool results. |
| G-10 | Legacy Jcode compatibility | The legacy initialize flow lists tools and successfully routes a fake-provider `web_fetch` call. |
| G-11 | Fetch projections | Headings, links, offsets, continuation, raw conflicts, and UTF-8 output budgets match the MCP contract. |
| G-12 | Logging | Default logging is silent; enabled logs include method, request ID, latency, outcome, and no credentials. |
| G-13 | Explorer contract | HTTP and subprocess explorer tests cover list, inspect, call, compact/full schemas, auth, shorthand JSON values, tool errors, and output budgets. |
| G-14 | Controlled browser render | When `WEBFETCH_TEST_CHROME_PATH` is set, a local JavaScript fixture passes through chromedp, the loopback proxy, and Defuddle, returning rendered Markdown and metadata. |

## Execution order

The document is intentionally ordered so that a broken install is not hidden by
in-process tests:

```bash
# 1. Install and identify the current checkout's binary.
go install ./cmd/webfetch
BIN_DIR="$(go env GOBIN)"
if [ -z "$BIN_DIR" ]; then BIN_DIR="$(go env GOPATH)/bin"; fi
BIN="$BIN_DIR/webfetch"

# 2. Run the installed-binary stdio and HTTP suites.
python3 -B scripts/mcp-smoke-test.py --binary "$BIN" --mode full
python3 -B scripts/mcp-smoke-test.py --binary "$BIN" --mode compact
python3 -B scripts/mcp-smoke-test.py --binary "$BIN" --mode http

# 3. Dogfood the explorer against an already-running endpoint and local commands.
#    The installed dogfood run uses loopback fixtures and is recorded below.
"$BIN" mcp list http://127.0.0.1:8787
"$BIN" mcp inspect http://127.0.0.1:8787 web_fetch
"$BIN" mcp call http://127.0.0.1:8787 web_fetch -a url http://127.0.0.1:1/blocked
"$BIN" mcp list --command "$BIN" --arg --mcp

# 4. Run the in-process MCP and fake-provider cases.
go test ./cmd/webfetch -run 'Test(MCP|RunMCPExplorer|ParseMCPExplorer)' -count=1

# 5. Optional real-browser integration. Normal tests do not require Chrome.
WEBFETCH_TEST_CHROME_PATH="/path/to/Chrome" \
  go test ./internal/webfetch -run 'TestChromedpRendererControlledPage|TestServiceFetchControlledRender' -count=1 -v

# 6. Run repository quality gates.
gofmt -w cmd/webfetch/main.go cmd/webfetch/mcp.go cmd/webfetch/mcp_compact.go cmd/webfetch/mcp_compat.go cmd/webfetch/mcp_test.go
go build ./...
go test ./... -count=1
go test -race ./...
go vet ./...
go mod verify
just build
git diff --check
```

The script and tests must be deterministic, bounded, and safe to run without
credentials. A test that needs an external API key is not part of this smoke
suite.

## Pass/fail rules

The smoke suite passes only when all of the following are true:

- All B-01 through B-15 cases pass.
- All C-01 through C-13 cases pass.
- All G-01 through G-13 cases pass. G-14 is required when a Chrome test path is
  available and otherwise is explicitly recorded as skipped.
- The installed binary exits cleanly after EOF.
- No stdout line contains logs, prompts, or non-JSON data.
- No required check depends on an external network service.
- Build, tests, race detection, vet, module verification, and diff checks pass.

Any protocol mismatch, unexpected stdout, failed safety rejection, hanging
process, or unbounded test behavior is a release-blocking failure for this
integration. Record the exact case ID, environment, command, request, response,
stderr, exit status, and reproducibility before changing the implementation.

## Recorded run (historical)

This section preserves a prior installed-binary run. It is historical evidence,
not proof for the current checkout; replace the complete record only after
rerunning the contract against one attributable installed binary.

| Field | Value |
| --- | --- |
| Date (UTC) | 2026-08-07, full/compact/HTTP and explorer installed-artifact run |
| OS / architecture | macOS arm64 |
| Go version | go1.26.5 |
| Binary path | `./webfetch` |
| Binary version | `5d0f352-dirty` from the historical checkout |
| Black-box result | **PASS:** B-01 through B-13, C-01 through C-13, and H-01 through H-06; full, compact, and HTTP runs reported their passing markers |
| In-process result | **PASS:** G-01 through G-13 via the repository suite; legacy coverage includes `TestMCPServerAcceptsLegacyJcodeHandshake` and explorer coverage includes HTTP, stdio, compact, auth, shorthand arguments, errors, and budgets |
| Repository gates | **PASS:** `go test ./...`, `go vet ./...`, `go build ./...`, `just build`, and `git diff --check` |
| Mode selectors | **PASS:** `--mcp --compact`, `--mcp-compact`, and `WEBFETCH_MCP_TOOL_MODE=compact` each exposed only `webfetch` |
| stderr review | Empty during the installed-binary protocol and HTTP runs; every stdio stdout line was JSON-RPC 2.0 |
| Jcode integration result | **PASS:** Jcode connected to `webfetch` and registered `mcp__webfetch__web_fetch` plus `mcp__webfetch__web_search` |
| Explorer dogfood | **PASS:** installed `list`, `inspect`, and `call` against full and compact loopback HTTP servers plus local full/compact stdio commands; legacy initialize was verified separately with the installed binary |
| Known failures | None; HTTP endpoint is the root listener URL, for example `http://127.0.0.1:8787` |

## Related sources

- [MCP integration contract](mcp.md)
- [MCP 2026-07-28 specification](https://modelcontextprotocol.io/specification/2026-07-28/)
- [MCP stdio transport](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/stdio)
- [MCP server tools](https://modelcontextprotocol.io/specification/2026-07-28/server/tools)
