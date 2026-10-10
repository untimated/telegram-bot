package telegrambot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeminiClientAnswersDirectlyWithSearch(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "key" {
			t.Errorf("missing api key header")
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"steps":[{"type":"model_output","content":[{"text":"It is sunny.","annotations":[{"type":"url_citation","url":"https://example.com/w","title":"Weather"}]}]}]}`))
	}))
	defer server.Close()

	client := &geminiClient{apiKey: "key", baseURL: server.URL, model: "m", search: true, http: server.Client()}
	answer, err := client.complete(context.Background(), []chatMessage{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: []chatContentPart{
			{Type: "text", Text: "what is this?"},
			{Type: "image_url", ImageURL: &chatImageURL{URL: "data:image/jpeg;base64,QUJD"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "It is sunny.") || !strings.Contains(answer, "[Weather](https://example.com/w)") {
		t.Errorf("answer = %q", answer)
	}
	if got["system_instruction"] != "be brief" || got["tools"] == nil {
		t.Errorf("request = %v", got)
	}
	if parts := got["input"].([]any); len(parts) != 2 || parts[1].(map[string]any)["mime_type"] != "image/jpeg" {
		t.Errorf("input = %v", got["input"])
	}
}

func TestLoadConfigGeminiProviderNeedsOnlyGeminiKey(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "t")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("MODEL_PROVIDER", "gemini")
	t.Setenv("GEMINI_API_KEY", "")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Fatalf("err = %v", err)
	}
	t.Setenv("GEMINI_API_KEY", "g")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
}

func TestGeminiClientRunsImageToolCall(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		requests = append(requests, req)
		switch {
		case req["model"] == "img":
			w.Write([]byte(`{"steps":[{"type":"model_output","content":[{"type":"image","mime_type":"image/jpeg","data":"QUJD"}]}]}`))
		case req["previous_interaction_id"] == nil:
			w.Write([]byte(`{"id":"i1","steps":[{"type":"function_call","id":"c1","name":"generate_image","arguments":{"prompt":"a BYD Seal"}}]}`))
		default:
			w.Write([]byte(`{"id":"i2","steps":[{"type":"model_output","content":[{"type":"text","text":"Here you go."}]}]}`))
		}
	}))
	defer server.Close()

	client := &geminiClient{apiKey: "k", baseURL: server.URL, model: "m", imageModel: "img", images: true, http: server.Client()}
	reply, err := client.completeReply(context.Background(), []chatMessage{{Role: "user", Content: "draw a BYD Seal"}})
	if err != nil {
		t.Fatal(err)
	}
	if reply.text != "Here you go." || len(reply.images) != 1 || string(reply.images[0].data) != "ABC" {
		t.Fatalf("reply = %+v", reply)
	}
	last := requests[len(requests)-1]
	if last["previous_interaction_id"] != "i1" {
		t.Errorf("follow-up request = %v", last)
	}
}
