# staan-mcp

A minimal Go [MCP](https://modelcontextprotocol.io) server that wraps the
[Staan](https://staan.ai) web search API (Qwant's European search index) as
a single `web_search` tool, for use as a web-search extension in
[goose](https://github.com/aaif-goose/goose) or any other MCP-compatible
agent.

## Build

```bash
go build -o staan-mcp .
```

## Get a Staan API key

1. Sign up at [staan.ai](https://staan.ai) — the first 1,000 requests/month
   are free.
2. Copy your API key and export it as `STAAN_API_KEY` (see below). The
   server refuses to start without it.

## Run

```bash
export STAAN_API_KEY=your-key-here
./staan-mcp
```

Optional flag:

- `-timeout` — HTTP client timeout for Staan API requests (default `10s`).
  Accepts Go duration syntax, e.g. `-timeout 15s`.

The server speaks MCP over stdio and exposes one tool:

### `web_search`

| Parameter | Type | Required | Description |
|---|---|---|---|
| `query` | string | yes | Search query, max 400 characters. Supports `site:` / `-site:` operators. |
| `market` | string | no | `fr-fr`, `en-us`, or `de-de`. Defaults to `fr-fr`. |
| `include_domains` | string[] | no | Restrict results to these domains (max 10). Mutually exclusive with `exclude_domains`. |
| `exclude_domains` | string[] | no | Exclude these domains from results (max 10). |
| `ai_search` | boolean | no | Use the "Web Search for AI" tier: adds relevance-reranked snippet excerpts and full page content (Markdown). Higher latency/cost than basic search. Default `false`. |

Results are returned as clean structured text (title, URL, snippet, and —
when `ai_search` is on — scored excerpts and full page content), not raw
JSON. HTTP errors, rate limits (429), and malformed responses are surfaced
as MCP tool errors rather than crashes.

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

## Development

```bash
go test ./...
```

Tests use `httptest.Server` to exercise the full request/response path
(including the AI tier and error handling) without making real network
calls to Staan.
