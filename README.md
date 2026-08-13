# staan-mcp

A minimal Go [MCP](https://modelcontextprotocol.io) server that wraps the
[Staan](https://staan.ai) web search API (Qwant's European search index) as
a single `web_search` tool, for use as a web-search extension in
[goose](https://github.com/aaif-goose/goose) or any other MCP-compatible
agent.

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

## Get a Staan API key

1. Sign up at [staan.ai](https://staan.ai) — the first 1,000 requests/month
   are free.
2. Copy your API key and export it as `STAAN_API_KEY` (see below). The
   server refuses to start without it.

## Configuration

| Setting | How | Default | Notes |
|---|---|---|---|
| Staan API key | env var `STAAN_API_KEY` | — (required) | Read once at startup. The server prints an error to stderr and exits with status 1 if it's unset. |
| HTTP client timeout | flag `-timeout` | `10s` | Go duration syntax, e.g. `-timeout 15s`. Applies to every request to the Staan API. Enriched (`ai_search=true`) searches are slower — the Staan docs recommend 8–10s for those, so raise this if you see timeouts with `ai_search` enabled. |

## Run

```bash
export STAAN_API_KEY=your-key-here
./staan-mcp
```

The server speaks MCP over stdio: it reads JSON-RPC requests from stdin and
writes responses to stdout. It's meant to be launched by an MCP client (like
goose), not run interactively.

### `web_search`

| Parameter | Type | Required | Description |
|---|---|---|---|
| `query` | string | yes | Search query, max 400 characters. Supports `site:` / `-site:` operators. |
| `market` | string (enum) | no | One of `fr-fr`, `en-us`, `de-de`. Defaults to `fr-fr`. |
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

With `ai_search=true`, each result additionally carries a `Published:` date
(when known), a `Relevant excerpts:` block of scored passages, and a
`Full content:` block with the fetched page body.

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

## Register with goose

Run `goose configure`, choose **Add Extension** → **Command-line Extension**,
and provide:

- **Extension name**: `staan` (or any name you like)
- **Command**: the full path to the built binary, e.g.
  `/path/to/staan-mcp/staan-mcp`
- **Environment variable**: `STAAN_API_KEY` — goose will prompt you to
  enter the value, which it stores and injects when launching the server.

Once added, goose can call the `web_search` tool automatically whenever a
task benefits from web search.

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
network calls to Staan. `main()`'s fail-fast behavior (missing
`STAAN_API_KEY`) is tested by re-executing the test binary as a subprocess,
since it calls `os.Exit`.

No real Staan API key is required to build, test, or vet this project.
