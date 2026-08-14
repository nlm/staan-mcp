# staan-mcp

A minimal Go [MCP](https://modelcontextprotocol.io) server that wraps the
[Staan](https://staan.ai) web search API (Qwant's European search index) as
a single `web_search` tool, for use as a web-search extension in any
MCP-compatible agent.

## Features

- Single `web_search` tool covering both Staan tiers: basic web search, and
  the enriched "Web Search for AI" tier (relevance-reranked excerpts + full
  page content in Markdown).
- Domain include/exclude filtering, market (language/region) selection.
- Results rendered as clean, structured text for direct LLM consumption —
  no raw JSON dumped into the model's context.
- HTTP errors, rate limits, and malformed responses are returned as MCP
  tool errors, never as a crash.
- Single static binary, one external dependency
  ([`mark3labs/mcp-go`](https://github.com/mark3labs/mcp-go)).

## Requirements

- Go 1.25 or later (see `go.mod`) to build from source.
- A Staan API key (see below).

## Build

```bash
go build -o staan-mcp .
```

This produces a single self-contained binary, `staan-mcp`, with no runtime
dependencies beyond network access to `api.staan.ai`.

Prebuilt binaries for Linux and macOS (amd64 and arm64) are published on the
[Releases](../../releases) page for every tagged version — download the
archive for your platform instead of building from source if you prefer.

## Get a Staan API key

1. Sign up at [staan.ai](https://staan.ai) — the first 1,000 requests/month
   are free.
2. Copy your API key and export it as `STAAN_API_KEY` (see below). The
   server refuses to start without it.

## Configuration

| Setting | How | Default | Notes |
|---|---|---|---|
| Staan API key | env var `STAAN_API_KEY` | — (required) | Read once at startup. The server prints an error to stderr and exits with status 1 if it's unset. |
| HTTP client timeout | env var `STAAN_TIMEOUT`, or flag `-timeout` | `10s` | Go duration syntax, e.g. `STAAN_TIMEOUT=15s` or `-timeout 15s`. Must be greater than zero — a zero or negative value fails fast at startup rather than disabling the timeout. The flag takes precedence if both are set — most MCP clients only let you configure command-line extensions via environment variables, so `STAAN_TIMEOUT` is usually the one you want. Applies to every request to the Staan API. Enriched (`ai_search=true`) searches are slower — the Staan docs recommend 8–10s for those, so raise this if you see timeouts with `ai_search` enabled. |
| Default market | env var `STAAN_DEFAULT_MARKET` | `fr-fr` (Staan's own default) | One of `fr-fr`, `en-us`, `de-de`. Used whenever a `web_search` call omits `market`. Invalid values fail fast at startup. The `web_search` tool's description is generated at startup to reflect the configured default, so the calling LLM sees the right value. |

## Run

```bash
export STAAN_API_KEY=your-key-here
./staan-mcp
```

With the optional settings:

```bash
export STAAN_API_KEY=your-key-here
export STAAN_TIMEOUT=15s
export STAAN_DEFAULT_MARKET=en-us
./staan-mcp
```

The server speaks MCP over stdio: it reads JSON-RPC requests from stdin and
writes responses to stdout. It's meant to be launched by an MCP client, not
run interactively.

### `web_search`

| Parameter | Type | Required | Description |
|---|---|---|---|
| `query` | string | yes | Search query, max 400 characters. Supports `site:` / `-site:` operators. |
| `market` | string (enum) | no | One of `fr-fr`, `en-us`, `de-de`. Defaults to `fr-fr`, or to `STAAN_DEFAULT_MARKET` if configured. |
| `include_domains` | string[] | no | Restrict results to these bare hostnames (max 10, e.g. `["qdrant.tech"]`). Mutually exclusive with `exclude_domains`. |
| `exclude_domains` | string[] | no | Exclude these bare hostnames from results (max 10). |
| `ai_search` | boolean | no | Use the "Web Search for AI" tier: adds relevance-reranked snippet excerpts and full page content (Markdown). Higher latency/cost than basic search. Default `false`. |

Example output for a basic search:

```
Found 3 result(s) for "open source vector databases":

1. Best Vector Databases in 2026
   URL: https://example.com/vector-dbs
   Snippet: Compare 20 vector databases with real performance benchmarks...

2. ...
```

Each result carries a `Published:` date when Staan reports one, regardless of
`ai_search`. With `ai_search=true`, each result additionally carries a
`Relevant excerpts:` block of scored passages and a `Full content:` block
with the fetched page body (capped at 25,000 characters, truncated with a
note if the page is longer) wrapped in explicit `BEGIN`/`END untrusted
external content` markers — the fetched page is data, not instructions, and
should be treated as untrusted by the calling model.

### Error handling

Errors never crash the process — they're returned as MCP tool errors, so the
calling LLM sees a descriptive message instead of the connection dropping:

- **Validation errors** (empty/oversized query, too many domain filters,
  both `include_domains` and `exclude_domains` set) are caught before any
  network call.
- **HTTP errors** from the Staan API are mapped to specific messages: 401/403
  → authentication failure, 429 → rate limit (Staan's limit is 20 req/s),
  400 → bad request, 5xx → server error, anything else → a generic message
  including the status code and response body.
- **Network failures** (DNS, connection refused, timeout) and **malformed
  JSON responses** are also caught and surfaced as tool errors.

### Logging

Every `web_search` call writes one summary line to stderr: query length,
market, `ai_search`, outcome, duration, result count, and the Staan
`search_id` (for cross-referencing with Staan support). The raw query text
and the API key are never logged. stdout is reserved for the MCP JSON-RPC
protocol, so all logging goes to stderr — most MCP clients surface a
server's stderr in their own logs.

## Register with an MCP client

Configuration is client-specific, but every MCP client that supports
command-line (stdio) extensions needs the same three things:

- **Command**: the full path to the built binary, e.g.
  `/path/to/staan-mcp/staan-mcp`
- **Environment variable**: `STAAN_API_KEY`, set to your Staan API key
- Optionally, `STAAN_TIMEOUT` and `STAAN_DEFAULT_MARKET` — see
  [Configuration](#configuration). Since most clients only expose
  environment variables (not CLI flags) for stdio extensions, these are
  generally easier to set here than `-timeout`.

Once registered, the client can call the `web_search` tool automatically
whenever a task benefits from web search.

## Project structure

```
staan-mcp/
├── main.go       # server setup, tool schema, Staan API client, formatting
├── main_test.go  # unit tests (httptest-based, no real network calls)
├── go.mod / go.sum
└── README.md
```

Everything lives in `package main` — there's deliberately no internal
package layout for a program this size.

## Development

```bash
go build -o staan-mcp .   # build
go vet ./...              # static checks
go test ./...             # run the test suite
go test -cover ./...      # run with coverage summary
```

Tests use `httptest.Server` (and a couple of custom `http.RoundTripper`s for
network-failure cases) to exercise the full request/response path — including
the AI tier, HTTP error mapping, and input validation — without making real
network calls to Staan. `main()`'s fail-fast behaviors (missing
`STAAN_API_KEY`, an invalid or non-positive `STAAN_TIMEOUT`, an invalid
`STAAN_DEFAULT_MARKET`) are tested by re-executing the test binary as a
subprocess, since they call `os.Exit`.

No real Staan API key is required to build, test, or vet this project.

## License

[MIT](LICENSE)
