package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *staanClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	old := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = old })

	return &staanClient{
		apiKey: "test-key",
		http:   &http.Client{Timeout: 5 * time.Second},
	}
}

func callToolText(t *testing.T, c *staanClient, args webSearchArgs) string {
	t.Helper()
	res, err := c.handleWebSearch(context.Background(), mcp.CallToolRequest{}, args)
	if err != nil {
		t.Fatalf("handler returned unexpected Go error: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("tool result has no content")
	}
	tc, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("expected text content, got %T", res.Content[0])
	}
	return tc.Text
}

func TestBasicSearchHappyPath(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization header = %q, want Bearer test-key", got)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		var body staanRequest
		json.NewDecoder(r.Body).Decode(&body)
		if body.Q != "open source llms" {
			t.Errorf("q = %q", body.Q)
		}
		if body.ExtraSnippets || body.FullContent != "" {
			t.Errorf("basic search should not set enrichment params, got %+v", body)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(staanResponse{
			SearchID: "01906c9e-7e3f-7000-8000-abc123def456",
			Web: struct {
				Results []staanResult `json:"results"`
			}{Results: []staanResult{
				{
					Title:      "Top open-source LLMs in 2024",
					URL:        "https://www.example.com/open-source-llms",
					Snippet:    "A comprehensive guide to the best open-source large language models...",
					DisplayURL: "www.example.com > ai > open-source-llms",
					Hostname:   "www.example.com",
				},
			}},
		})
	})

	out := callToolText(t, c, webSearchArgs{Query: "open source llms", Market: "en-us"})

	for _, want := range []string{"Top open-source LLMs in 2024", "https://www.example.com/open-source-llms", "A comprehensive guide"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Relevant excerpts") || strings.Contains(out, "Full content") {
		t.Errorf("basic search output should not include AI-tier sections:\n%s", out)
	}
}

func TestDefaultMarketAppliedWhenArgOmitted(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body staanRequest
		json.NewDecoder(r.Body).Decode(&body)
		if body.Market != "de-de" {
			t.Errorf("market = %q, want de-de (from defaultMarket)", body.Market)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(staanResponse{})
	})
	c.defaultMarket = "de-de"

	callToolText(t, c, webSearchArgs{Query: "test"})
}

func TestExplicitMarketOverridesDefault(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body staanRequest
		json.NewDecoder(r.Body).Decode(&body)
		if body.Market != "en-us" {
			t.Errorf("market = %q, want en-us (explicit arg should win)", body.Market)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(staanResponse{})
	})
	c.defaultMarket = "de-de"

	callToolText(t, c, webSearchArgs{Query: "test", Market: "en-us"})
}

func TestAISearchHappyPath(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body staanRequest
		json.NewDecoder(r.Body).Decode(&body)
		if !body.ExtraSnippets {
			t.Errorf("ai_search=true should set extra_snippets=true")
		}
		if body.FullContent != "markdown" {
			t.Errorf("ai_search=true should set full_content=markdown, got %q", body.FullContent)
		}

		w.Header().Set("Content-Type", "application/json")
		resp := staanResponse{}
		resp.Web.Results = []staanResult{
			{
				Title:   "Comparing vector databases in 2024",
				URL:     "https://www.example.com/vector-dbs",
				Snippet: "A deep dive into Pinecone, Weaviate, Qdrant...",
			},
		}
		resp.Web.Results[0].ExtraSnippets = []struct {
			Chunk string  `json:"chunk"`
			Score float64 `json:"score"`
		}{
			{Chunk: "Pinecone is a fully managed vector database...", Score: 0.91},
		}
		resp.Web.Results[0].FullContent = &struct {
			Text   string `json:"text"`
			Format string `json:"format"`
			Length int    `json:"length"`
		}{Text: "# Comparing vector databases\n\nThis guide covers...", Format: "markdown", Length: 22100}
		json.NewEncoder(w).Encode(resp)
	})

	out := callToolText(t, c, webSearchArgs{Query: "vector database comparison", AISearch: true})

	for _, want := range []string{"Relevant excerpts", "score 0.91", "Pinecone is a fully managed", "Full content (markdown, 22100 chars)", "This guide covers"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestStatus201TreatedAsSuccess(t *testing.T) {
	// The live Staan API returns 201 Created on a successful search, not 200 OK.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(staanResponse{
			Web: struct {
				Results []staanResult `json:"results"`
			}{Results: []staanResult{{Title: "T", URL: "https://example.com", Snippet: "S"}}},
		})
	})

	out := callToolText(t, c, webSearchArgs{Query: "test"})
	if !strings.Contains(out, "https://example.com") {
		t.Errorf("expected 201 response to be treated as success, got:\n%s", out)
	}
}

func TestAuthErrorMappedToToolError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"invalid API key"}`))
	})

	res, err := c.handleWebSearch(context.Background(), mcp.CallToolRequest{}, webSearchArgs{Query: "test"})
	if err != nil {
		t.Fatalf("expected Go error to be nil (tool error, not crash), got %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for 401 response")
	}
	tc, _ := mcp.AsTextContent(res.Content[0])
	if !strings.Contains(tc.Text, "authentication failed") {
		t.Errorf("expected auth error message, got %q", tc.Text)
	}
}

func TestRateLimitErrorMappedToToolError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`rate limited`))
	})

	res, _ := c.handleWebSearch(context.Background(), mcp.CallToolRequest{}, webSearchArgs{Query: "test"})
	if !res.IsError {
		t.Fatalf("expected IsError=true for 429 response")
	}
	tc, _ := mcp.AsTextContent(res.Content[0])
	if !strings.Contains(tc.Text, "rate limit") {
		t.Errorf("expected rate limit message, got %q", tc.Text)
	}
}

func TestMalformedJSONMappedToToolError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{not json`))
	})

	res, err := c.handleWebSearch(context.Background(), mcp.CallToolRequest{}, webSearchArgs{Query: "test"})
	if err != nil {
		t.Fatalf("expected Go error to be nil (tool error, not crash), got %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for malformed JSON")
	}
}

func TestServerUnreachableMappedToToolError(t *testing.T) {
	c := &staanClient{apiKey: "test-key", http: &http.Client{Timeout: 2 * time.Second}}
	old := apiBaseURL
	apiBaseURL = "http://127.0.0.1:1" // nothing listening
	t.Cleanup(func() { apiBaseURL = old })

	res, err := c.handleWebSearch(context.Background(), mcp.CallToolRequest{}, webSearchArgs{Query: "test"})
	if err != nil {
		t.Fatalf("expected Go error to be nil (tool error, not crash), got %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true when server is unreachable")
	}
}

func TestValidationErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("API should not be called when validation fails")
	})

	cases := []struct {
		name string
		args webSearchArgs
	}{
		{"empty query", webSearchArgs{Query: "  "}},
		{"query too long", webSearchArgs{Query: strings.Repeat("a", 401)}},
		{"too many include domains", webSearchArgs{Query: "q", IncludeDomains: make([]string, 11)}},
		{"too many exclude domains", webSearchArgs{Query: "q", ExcludeDomains: make([]string, 11)}},
		{"mutually exclusive domains", webSearchArgs{Query: "q", IncludeDomains: []string{"a.com"}, ExcludeDomains: []string{"b.com"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := c.handleWebSearch(context.Background(), mcp.CallToolRequest{}, tc.args)
			if err != nil {
				t.Fatalf("expected Go error to be nil, got %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected IsError=true for %s", tc.name)
			}
		})
	}
}

func TestApiErrorMessages(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		body         string
		wantContains string
	}{
		{"bad request", http.StatusBadRequest, `{"message":"query too long"}`, "rejected the request (status 400)"},
		{"server error", http.StatusInternalServerError, "boom", "server error (status 500)"},
		{"unexpected status", http.StatusNotFound, "not found", "unexpected status 404"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := apiError(tc.status, []byte(tc.body))
			if !strings.Contains(err.Error(), tc.wantContains) {
				t.Errorf("apiError(%d, ...) = %q, want substring %q", tc.status, err.Error(), tc.wantContains)
			}
		})
	}
}

func TestApiErrorTruncatesLongBody(t *testing.T) {
	body := strings.Repeat("x", 1000)
	err := apiError(http.StatusInternalServerError, []byte(body))
	msg := err.Error()

	if !strings.Contains(msg, strings.Repeat("x", 500)+"...") {
		t.Errorf("expected body truncated to 500 chars followed by '...', got: %s", msg)
	}
	if strings.Contains(msg, strings.Repeat("x", 501)) {
		t.Errorf("body was not truncated: %s", msg)
	}
}

func TestFormatResultsEmpty(t *testing.T) {
	out := formatResults(&staanResponse{}, false)
	if out != "No results found." {
		t.Errorf("formatResults with no results = %q, want %q", out, "No results found.")
	}
}

func TestFormatResultsAlteredQuery(t *testing.T) {
	r := &staanResponse{}
	r.Query.Q = "climat tech"
	r.Query.AlteredQuery = "climate tech"
	r.Web.Results = []staanResult{{Title: "T", URL: "https://example.com"}}

	out := formatResults(r, false)
	if !strings.Contains(out, `search engine used: "climate tech"`) {
		t.Errorf("expected altered query note in output:\n%s", out)
	}
}

func TestFormatResultsPublishedDate(t *testing.T) {
	r := &staanResponse{}
	r.Web.Results = []staanResult{{Title: "T", URL: "https://example.com", PublishedAt: "2024-01-01T00:00:00Z"}}

	out := formatResults(r, true)
	if !strings.Contains(out, "Published: 2024-01-01T00:00:00Z") {
		t.Errorf("expected published date in output:\n%s", out)
	}
}

func TestIsValidMarket(t *testing.T) {
	for _, m := range validMarkets {
		if !isValidMarket(m) {
			t.Errorf("isValidMarket(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"", "xx-xx", "FR-FR", "fr_fr"} {
		if isValidMarket(m) {
			t.Errorf("isValidMarket(%q) = true, want false", m)
		}
	}
}

func TestMarketDescriptionDefault(t *testing.T) {
	if got := marketDescriptionDefault(""); got != "fr-fr" {
		t.Errorf("marketDescriptionDefault(\"\") = %q, want %q", got, "fr-fr")
	}
	if got := marketDescriptionDefault("de-de"); got != "de-de (server-configured)" {
		t.Errorf("marketDescriptionDefault(\"de-de\") = %q, want %q", got, "de-de (server-configured)")
	}
}

// errReader always fails, simulating a connection dropping mid-response-body.
type errReader struct{}

func (errReader) Read(p []byte) (int, error) { return 0, errors.New("connection reset") }

type errorBodyTransport struct{}

func (errorBodyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(errReader{}),
		Header:     make(http.Header),
	}, nil
}

func TestSearchResponseBodyReadError(t *testing.T) {
	c := &staanClient{apiKey: "test-key", http: &http.Client{Transport: errorBodyTransport{}}}

	res, err := c.handleWebSearch(context.Background(), mcp.CallToolRequest{}, webSearchArgs{Query: "test"})
	if err != nil {
		t.Fatalf("expected Go error to be nil (tool error, not crash), got %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true when the response body fails to read")
	}
	tc, _ := mcp.AsTextContent(res.Content[0])
	if !strings.Contains(tc.Text, "failed to read Staan API response") {
		t.Errorf("expected read-failure message, got %q", tc.Text)
	}
}

// runMainSubprocess re-executes this test binary with main() invoked in a
// child process, so tests can exercise main()'s os.Exit paths (which can't
// be reached from an in-process test). unsetEnv lists variable names to
// strip from the child's environment before it starts; setEnv is applied on
// top of that.
func runMainSubprocess(t *testing.T, testName string, unsetEnv []string, setEnv map[string]string) (exitCode int, stderr string) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^"+testName+"$")
	env := []string{"STAAN_MCP_TEST_RUN_MAIN=1"}
outer:
	for _, e := range os.Environ() {
		for _, u := range unsetEnv {
			if strings.HasPrefix(e, u+"=") {
				continue outer
			}
		}
		env = append(env, e)
	}
	for k, v := range setEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	var buf bytes.Buffer
	cmd.Stderr = &buf

	err := cmd.Run()
	if err == nil {
		return 0, buf.String()
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected the process to exit normally or with *exec.ExitError, got: %v", err)
	}
	return exitErr.ExitCode(), buf.String()
}

func TestMainMissingAPIKeyFailsFast(t *testing.T) {
	if os.Getenv("STAAN_MCP_TEST_RUN_MAIN") == "1" {
		main()
		return
	}

	code, stderr := runMainSubprocess(t, "TestMainMissingAPIKeyFailsFast", []string{"STAAN_API_KEY"}, nil)

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "STAAN_API_KEY") {
		t.Errorf("stderr should mention STAAN_API_KEY, got: %s", stderr)
	}
}

func TestMainInvalidTimeoutFailsFast(t *testing.T) {
	if os.Getenv("STAAN_MCP_TEST_RUN_MAIN") == "1" {
		main()
		return
	}

	code, stderr := runMainSubprocess(t, "TestMainInvalidTimeoutFailsFast", nil, map[string]string{
		"STAAN_TIMEOUT": "not-a-duration",
	})

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "STAAN_TIMEOUT") {
		t.Errorf("stderr should mention STAAN_TIMEOUT, got: %s", stderr)
	}
}

func TestMainInvalidDefaultMarketFailsFast(t *testing.T) {
	if os.Getenv("STAAN_MCP_TEST_RUN_MAIN") == "1" {
		main()
		return
	}

	code, stderr := runMainSubprocess(t, "TestMainInvalidDefaultMarketFailsFast", nil, map[string]string{
		"STAAN_API_KEY":        "fake-key-for-startup-check",
		"STAAN_DEFAULT_MARKET": "xx-xx",
	})

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "STAAN_DEFAULT_MARKET") {
		t.Errorf("stderr should mention STAAN_DEFAULT_MARKET, got: %s", stderr)
	}
}
