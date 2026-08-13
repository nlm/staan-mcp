package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
