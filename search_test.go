package telegrambot

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// enableSearch turns the web_search tool on for one test.
func enableSearch(t *testing.T) {
	t.Helper()
	t.Setenv("GEMINI_API_KEY", "test-gemini-key")
}

// toolMessages returns the tool results in a request the model is being fed.
func toolMessages(t *testing.T, request map[string]any) []map[string]any {
	t.Helper()
	messages, ok := request["messages"].([]any)
	if !ok {
		t.Fatalf("request carried no messages: %v", request)
	}
	var results []map[string]any
	for _, message := range messages {
		entry, ok := message.(map[string]any)
		if !ok {
			continue
		}
		if entry["role"] == "tool" {
			results = append(results, entry)
		}
	}
	return results
}

func TestWebSearchRoundTrip(t *testing.T) {
	fake := newFakeUpstream(t)
	fake.searchText = "Spain won Euro 2024, beating England 2-1 in the final."
	fake.responses = []map[string]any{
		toolCallCompletion("call_1", webSearchToolName, `{"query":"who won euro 2024"}`),
		textCompletion("Spain won it, per [example](https://example.com/news)."),
	}
	setupBot(t, fake)
	enableSearch(t)

	if code := postUpdate(t, privateUpdate(4242, "who won euro 2024?"), "").Code; code != http.StatusOK {
		t.Fatalf("webhook status = %d, want %d", code, http.StatusOK)
	}

	requests := fake.deepSeekRequests()
	if len(requests) != 2 {
		t.Fatalf("deepseek calls = %d, want 2: the search, then the answer", len(requests))
	}

	// The tool is offered on the first call, and named as the model expects.
	tools, ok := requests[0]["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v, want one web_search tool", requests[0]["tools"])
	}
	function, _ := tools[0].(map[string]any)["function"].(map[string]any)
	if function["name"] != webSearchToolName {
		t.Errorf("tool name = %v, want %v", function["name"], webSearchToolName)
	}

	// The search ran, with Google Search grounding switched on.
	searches := fake.searchRequests()
	if len(searches) != 1 {
		t.Fatalf("searches = %d, want 1", len(searches))
	}
	if searches[0]["input"] != "who won euro 2024" {
		t.Errorf("search input = %v, want the model's query", searches[0]["input"])
	}
	searchTools, _ := searches[0]["tools"].([]any)
	if len(searchTools) != 1 || searchTools[0].(map[string]any)["type"] != "google_search" {
		t.Errorf("search tools = %v, want google_search grounding", searches[0]["tools"])
	}

	// The findings and their source were fed back as the tool result.
	results := toolMessages(t, requests[1])
	if len(results) != 1 {
		t.Fatalf("tool results = %d, want 1", len(results))
	}
	if results[0]["tool_call_id"] != "call_1" {
		t.Errorf("tool_call_id = %v, want call_1", results[0]["tool_call_id"])
	}
	content, _ := results[0]["content"].(string)
	if !strings.Contains(content, "Spain won Euro 2024") {
		t.Errorf("tool result = %q, want the grounded answer", content)
	}
	if !strings.Contains(content, "https://example.com/news") {
		t.Errorf("tool result = %q, want the cited source", content)
	}

	// The user receives the model's final answer, not the raw search output.
	sent := fake.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("telegram messages = %d, want 1", len(sent))
	}
	if !strings.Contains(sent[0].Text, "Spain won it") {
		t.Errorf("reply = %q, want the final answer", sent[0].Text)
	}
}

func TestNoSearchToolWithoutAGeminiKey(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)

	postUpdate(t, privateUpdate(4242, "hi"), "")

	requests := fake.deepSeekRequests()
	if len(requests) != 1 {
		t.Fatalf("deepseek calls = %d, want 1", len(requests))
	}
	if tools, ok := requests[0]["tools"]; ok {
		t.Errorf("tools = %v, want none when no Gemini key is configured", tools)
	}
	if searches := fake.searchRequests(); len(searches) != 0 {
		t.Errorf("searches = %d, want none", len(searches))
	}
}

func TestSearchFailureStillAnswers(t *testing.T) {
	fake := newFakeUpstream(t)
	fake.searchCode = http.StatusServiceUnavailable
	fake.responses = []map[string]any{
		toolCallCompletion("call_1", webSearchToolName, `{"query":"news"}`),
		textCompletion("I couldn't look that up, but here's what I know."),
	}
	setupBot(t, fake)
	enableSearch(t)

	postUpdate(t, privateUpdate(4242, "any news?"), "")

	results := toolMessages(t, fake.deepSeekRequests()[1])
	if len(results) != 1 {
		t.Fatalf("tool results = %d, want 1", len(results))
	}
	content, _ := results[0]["content"].(string)
	if !strings.Contains(content, "web search failed") {
		t.Errorf("tool result = %q, want the failure reported to the model", content)
	}

	sent := fake.sentMessages()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "here's what I know") {
		t.Fatalf("sent = %+v, want an answer instead of an apology", sent)
	}
}

func TestSearchRoundsAreBounded(t *testing.T) {
	fake := newFakeUpstream(t)
	fake.searchText = "endless results"
	for range maxSearchRounds + 2 {
		fake.responses = append(fake.responses, toolCallCompletion("call", webSearchToolName, `{"query":"again"}`))
	}
	setupBot(t, fake)
	enableSearch(t)

	postUpdate(t, privateUpdate(4242, "loop please"), "")

	if calls := len(fake.deepSeekRequests()); calls != maxSearchRounds+1 {
		t.Errorf("deepseek calls = %d, want the loop to stop after %d searches", calls, maxSearchRounds)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 || !strings.Contains(strings.ToLower(sent[0].Text), "try again") {
		t.Fatalf("sent = %+v, want the user told something went wrong", sent)
	}
}

func TestUnknownToolIsReportedToTheModel(t *testing.T) {
	fake := newFakeUpstream(t)
	fake.responses = []map[string]any{
		toolCallCompletion("call_9", "delete_everything", `{}`),
		textCompletion("I can't do that."),
	}
	setupBot(t, fake)
	enableSearch(t)

	postUpdate(t, privateUpdate(4242, "drop the database"), "")

	results := toolMessages(t, fake.deepSeekRequests()[1])
	if len(results) != 1 {
		t.Fatalf("tool results = %d, want 1", len(results))
	}
	if content, _ := results[0]["content"].(string); !strings.Contains(content, "unknown tool") {
		t.Errorf("tool result = %q, want the unknown tool named", content)
	}
	if searches := fake.searchRequests(); len(searches) != 0 {
		t.Errorf("searches = %d, want none for an unknown tool", len(searches))
	}
	if sent := fake.sentMessages(); len(sent) != 1 || !strings.Contains(sent[0].Text, "can't do that") {
		t.Fatalf("sent = %+v, want the model's answer", sent)
	}
}

type stubSearcher struct {
	query string
	err   error
}

func (s *stubSearcher) search(_ context.Context, query string) (string, error) {
	s.query = query
	if s.err != nil {
		return "", s.err
	}
	return "findings for " + query, nil
}

func TestRunToolRejectsBadCalls(t *testing.T) {
	tests := []struct {
		name   string
		client *deepSeekClient
		call   toolCall
		want   string
	}{
		{
			name:   "unknown tool",
			client: &deepSeekClient{search: &stubSearcher{}},
			call:   makeToolCall("nope", `{}`),
			want:   "unknown tool",
		},
		{
			name:   "malformed arguments",
			client: &deepSeekClient{search: &stubSearcher{}},
			call:   makeToolCall(webSearchToolName, `not json`),
			want:   "could not read the tool arguments",
		},
		{
			name:   "missing query",
			client: &deepSeekClient{search: &stubSearcher{}},
			call:   makeToolCall(webSearchToolName, `{"query":"   "}`),
			want:   "non-empty",
		},
		{
			name:   "search not configured",
			client: &deepSeekClient{},
			call:   makeToolCall(webSearchToolName, `{"query":"news"}`),
			want:   "not configured",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.client.runTool(context.Background(), test.call)
			if !strings.Contains(got, test.want) {
				t.Errorf("runTool() = %q, want it to mention %q", got, test.want)
			}
		})
	}
}

func TestRunToolRunsTheSearch(t *testing.T) {
	stub := &stubSearcher{}
	client := &deepSeekClient{search: stub}

	if got := client.runTool(context.Background(), makeToolCall(webSearchToolName, `{"query":" euro 2024 "}`)); got != "findings for euro 2024" {
		t.Errorf("runTool() = %q, want the search findings", got)
	}
	if stub.query != "euro 2024" {
		t.Errorf("search query = %q, want the query trimmed", stub.query)
	}
}

func TestRunToolSurfacesSearchFailures(t *testing.T) {
	client := &deepSeekClient{search: &stubSearcher{err: errors.New("boom")}}

	if got := client.runTool(context.Background(), makeToolCall(webSearchToolName, `{"query":"news"}`)); !strings.Contains(got, "boom") {
		t.Errorf("runTool() = %q, want the failure surfaced to the model", got)
	}
}

type deadlineSearcher struct {
	called   bool
	deadline time.Time
}

func (s *deadlineSearcher) search(ctx context.Context, _ string) (string, error) {
	s.called = true
	s.deadline, _ = ctx.Deadline()
	return "findings", nil
}

func TestRunToolLeavesTimeForFinalReply(t *testing.T) {
	call := makeToolCall(webSearchToolName, `{"query":"news"}`)
	t.Run("search has its own timeout", func(t *testing.T) {
		stub := &deadlineSearcher{}
		ctx, cancel := context.WithTimeout(context.Background(), replyBudget)
		defer cancel()
		parentDeadline, _ := ctx.Deadline()
		if got := (&deepSeekClient{search: stub}).runTool(ctx, call); got != "findings" {
			t.Fatalf("runTool() = %q, want findings", got)
		}
		if !stub.called || time.Until(stub.deadline) > webSearchTimeout || parentDeadline.Sub(stub.deadline) < finalReplyReserve-100*time.Millisecond {
			t.Fatalf("search deadline = %v, parent deadline = %v", stub.deadline, parentDeadline)
		}
	})
	t.Run("search is skipped near the reply deadline", func(t *testing.T) {
		stub := &deadlineSearcher{}
		ctx, cancel := context.WithTimeout(context.Background(), finalReplyReserve)
		defer cancel()
		got := (&deepSeekClient{search: stub}).runTool(ctx, call)
		if stub.called || !strings.Contains(got, "could not verify") {
			t.Fatalf("search called = %t, result = %q", stub.called, got)
		}
	})
}

func makeToolCall(name, arguments string) toolCall {
	var call toolCall
	call.ID = "call_1"
	call.Type = "function"
	call.Function.Name = name
	call.Function.Arguments = arguments
	return call
}
