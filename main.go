// Command staan-mcp is a Model Context Protocol server exposing Staan's
// (https://staan.ai) web search API as a single `web_search` tool, for use
// as a web-search extension in MCP-compatible agents.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	searchPath     = "/search/web"
	maxDomains     = 10
	defaultTimeout = 10 * time.Second
)

// apiBaseURL is a var (not const) so tests can point it at an httptest server.
var apiBaseURL = "https://api.staan.ai/v2"

// validMarkets are the market codes Staan accepts.
var validMarkets = []string{"fr-fr", "en-us", "de-de"}

func isValidMarket(m string) bool {
	for _, v := range validMarkets {
		if m == v {
			return true
		}
	}
	return false
}

func main() {
	timeoutDefault := defaultTimeout
	if v := os.Getenv("STAAN_TIMEOUT"); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			fmt.Fprintf(os.Stderr, "staan-mcp: invalid STAAN_TIMEOUT value %q: %v\n", v, err)
			os.Exit(1)
		}
		timeoutDefault = parsed
	}
	timeoutFlag := flag.Duration("timeout", timeoutDefault, "HTTP client timeout for Staan API requests (e.g. 10s, 15s). Overrides STAAN_TIMEOUT if both are set.")
	flag.Parse()

	apiKey := os.Getenv("STAAN_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "staan-mcp: STAAN_API_KEY environment variable is not set; get a key at https://staan.ai and export STAAN_API_KEY before starting this server")
		os.Exit(1)
	}

	defaultMarket := os.Getenv("STAAN_DEFAULT_MARKET")
	if defaultMarket != "" && !isValidMarket(defaultMarket) {
		fmt.Fprintf(os.Stderr, "staan-mcp: invalid STAAN_DEFAULT_MARKET value %q; must be one of %s\n", defaultMarket, strings.Join(validMarkets, ", "))
		os.Exit(1)
	}

	client := &staanClient{
		apiKey:        apiKey,
		http:          &http.Client{Timeout: *timeoutFlag},
		defaultMarket: defaultMarket,
	}

	s := server.NewMCPServer(
		"staan-mcp",
		"1.0.0",
		server.WithToolCapabilities(false),
	)

	tool := mcp.NewTool("web_search",
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
		mcp.WithDescription("Search the live web and return a ranked list of results (title, URL, snippet per result). "+
			"Use this when you need current information, facts, or sources from outside your training data — "+
			"news, prices, documentation, recent events, or anything you should cite rather than recall from memory. "+
			"Not for looking up information already present in this conversation or in files you can read directly. "+
			"Basic search (default) returns up to 10 results with title/URL/snippet only. "+
			"Set ai_search=true for the 'Web Search for AI' tier, which additionally fetches each result page and "+
			"returns relevance-reranked excerpts plus the full page body in Markdown — use this for research, "+
			"citation-heavy answers, or RAG-style tasks where you need more than a snippet; skip it for a quick "+
			"fact lookup since it fetches every result page and is noticeably slower."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.MaxLength(400),
			mcp.Description("The search query, e.g. \"open source vector databases\" or \"pricing site:qdrant.tech\". "+
				"Max 400 characters. Supports Google-style site: and -site: operators to include or exclude a "+
				"domain directly in the query, as an alternative to include_domains/exclude_domains."),
		),
		mcp.WithString("market",
			mcp.Enum("fr-fr", "en-us", "de-de"),
			mcp.Description(fmt.Sprintf("Language/region to search in, as a market code. Defaults to %s if omitted. "+
				"Use en-us for English-language results, de-de for German, fr-fr for French.", marketDescriptionDefault(client.defaultMarket))),
		),
		mcp.WithArray("include_domains",
			mcp.Description("Restrict results to only these domains, e.g. [\"qdrant.tech\", \"weaviate.io\"] "+
				"(bare hostnames, no scheme or path). Max 10. Use this when the caller (not the query author) "+
				"needs to enforce a source allowlist. Mutually exclusive with exclude_domains — do not set both."),
			mcp.Items(map[string]any{"type": "string"}),
		),
		mcp.WithArray("exclude_domains",
			mcp.Description("Exclude results from these domains, e.g. [\"reddit.com\", \"pinterest.com\"] "+
				"(bare hostnames, no scheme or path). Max 10. Mutually exclusive with include_domains."),
			mcp.Items(map[string]any{"type": "string"}),
		),
		mcp.WithBoolean("ai_search",
			mcp.Description("If true, use the 'Web Search for AI' tier: fetches each result's page and adds "+
				"relevance-reranked snippet excerpts plus the full page body (Markdown) to every result. "+
				"Higher latency and cost than basic search — enable it only when a snippet alone won't be "+
				"enough, e.g. summarizing an article, comparing sources in depth, or answering a question "+
				"that needs quotes from the page. Defaults to false (basic search)."),
			mcp.DefaultBool(false),
		),
	)

	s.AddTool(tool, mcp.NewTypedToolHandler(client.handleWebSearch))

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "staan-mcp: server error: %v\n", err)
		os.Exit(1)
	}
}

// webSearchArgs mirrors the web_search tool's input schema.
type webSearchArgs struct {
	Query          string   `json:"query"`
	Market         string   `json:"market"`
	IncludeDomains []string `json:"include_domains"`
	ExcludeDomains []string `json:"exclude_domains"`
	AISearch       bool     `json:"ai_search"`
}

type staanClient struct {
	apiKey        string
	http          *http.Client
	defaultMarket string // "" means fall through to the Staan API's own default (fr-fr)
}

// marketDescriptionDefault returns the effective default market for use in
// the tool's description text.
func marketDescriptionDefault(configured string) string {
	if configured != "" {
		return configured + " (server-configured)"
	}
	return "fr-fr"
}

// staanRequest is the JSON body sent to POST /v2/search/web.
type staanRequest struct {
	Q              string   `json:"q"`
	Market         string   `json:"market,omitempty"`
	IncludeDomains []string `json:"include_domains,omitempty"`
	ExcludeDomains []string `json:"exclude_domains,omitempty"`
	ExtraSnippets  bool     `json:"extra_snippets,omitempty"`
	FullContent    string   `json:"full_content,omitempty"`
}

// staanResponse is the JSON body returned by POST /v2/search/web.
type staanResponse struct {
	SearchID string `json:"search_id"`
	Query    struct {
		Q            string `json:"q"`
		AlteredQuery string `json:"altered_query"`
		Market       string `json:"market"`
		Count        int    `json:"count"`
		Offset       int    `json:"offset"`
	} `json:"query"`
	Web struct {
		Results []staanResult `json:"results"`
	} `json:"web"`
}

type staanResult struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	Snippet       string `json:"snippet"`
	DisplayURL    string `json:"display_url"`
	Hostname      string `json:"hostname"`
	FaviconURL    string `json:"favicon_url"`
	Thumbnail     string `json:"thumbnail"`
	PublishedAt   string `json:"published_date"`
	ExtraSnippets []struct {
		Chunk string  `json:"chunk"`
		Score float64 `json:"score"`
	} `json:"extra_snippets"`
	FullContent *struct {
		Text   string `json:"text"`
		Format string `json:"format"`
		Length int    `json:"length"`
	} `json:"full_content"`
}

func (c *staanClient) handleWebSearch(ctx context.Context, _ mcp.CallToolRequest, args webSearchArgs) (*mcp.CallToolResult, error) {
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return mcp.NewToolResultError("query is required and cannot be empty"), nil
	}
	if len(query) > 400 {
		return mcp.NewToolResultError("query must be 400 characters or fewer"), nil
	}
	if len(args.IncludeDomains) > maxDomains {
		return mcp.NewToolResultError(fmt.Sprintf("include_domains supports at most %d entries, got %d", maxDomains, len(args.IncludeDomains))), nil
	}
	if len(args.ExcludeDomains) > maxDomains {
		return mcp.NewToolResultError(fmt.Sprintf("exclude_domains supports at most %d entries, got %d", maxDomains, len(args.ExcludeDomains))), nil
	}
	if len(args.IncludeDomains) > 0 && len(args.ExcludeDomains) > 0 {
		return mcp.NewToolResultError("include_domains and exclude_domains are mutually exclusive"), nil
	}

	market := args.Market
	if market == "" {
		market = c.defaultMarket
	}

	reqBody := staanRequest{
		Q:              query,
		Market:         market,
		IncludeDomains: args.IncludeDomains,
		ExcludeDomains: args.ExcludeDomains,
	}
	if args.AISearch {
		reqBody.ExtraSnippets = true
		reqBody.FullContent = "markdown"
	}

	result, err := c.search(ctx, reqBody)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	return mcp.NewToolResultText(formatResults(result, args.AISearch)), nil
}

// search performs the POST request against the Staan search API and returns
// a decoded response, or a descriptive error suitable for surfacing to the
// calling LLM as a tool error (never a crash).
func (c *staanClient) search(ctx context.Context, body staanRequest) (*staanResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBaseURL+searchPath, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Staan API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read Staan API response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiError(resp.StatusCode, respBody)
	}

	var result staanResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("Staan API returned malformed JSON (status %d): %w", resp.StatusCode, err)
	}

	return &result, nil
}

// apiError turns a non-200 Staan response into a descriptive error, tailored
// to the well-known status codes documented for the API.
func apiError(status int, body []byte) error {
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 500 {
		snippet = snippet[:500] + "..."
	}

	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("Staan API authentication failed (status %d) - check that STAAN_API_KEY is valid: %s", status, snippet)
	case http.StatusTooManyRequests:
		return fmt.Errorf("Staan API rate limit exceeded (status 429, limit is 20 req/s) - retry with backoff: %s", snippet)
	case http.StatusBadRequest:
		return fmt.Errorf("Staan API rejected the request (status 400) - check query length and parameters: %s", snippet)
	default:
		if status >= 500 {
			return fmt.Errorf("Staan API server error (status %d) - try again later: %s", status, snippet)
		}
		return fmt.Errorf("Staan API returned unexpected status %d: %s", status, snippet)
	}
}

// formatResults renders a staanResponse as clean structured text for direct
// LLM consumption, rather than raw JSON.
func formatResults(r *staanResponse, aiSearch bool) string {
	if len(r.Web.Results) == 0 {
		return "No results found."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d result(s) for %q", len(r.Web.Results), r.Query.Q)
	if r.Query.AlteredQuery != "" && r.Query.AlteredQuery != r.Query.Q {
		fmt.Fprintf(&b, " (search engine used: %q)", r.Query.AlteredQuery)
	}
	b.WriteString(":\n\n")

	for i, res := range r.Web.Results {
		fmt.Fprintf(&b, "%d. %s\n", i+1, res.Title)
		fmt.Fprintf(&b, "   URL: %s\n", res.URL)
		if res.Snippet != "" {
			fmt.Fprintf(&b, "   Snippet: %s\n", res.Snippet)
		}
		if res.PublishedAt != "" {
			fmt.Fprintf(&b, "   Published: %s\n", res.PublishedAt)
		}

		if aiSearch && len(res.ExtraSnippets) > 0 {
			b.WriteString("   Relevant excerpts:\n")
			for _, s := range res.ExtraSnippets {
				fmt.Fprintf(&b, "     - [score %.2f] %s\n", s.Score, s.Chunk)
			}
		}

		if aiSearch && res.FullContent != nil && res.FullContent.Length > 0 {
			fmt.Fprintf(&b, "   Full content (%s, %d chars):\n", res.FullContent.Format, res.FullContent.Length)
			for _, line := range strings.Split(res.FullContent.Text, "\n") {
				fmt.Fprintf(&b, "     %s\n", line)
			}
		}

		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}
