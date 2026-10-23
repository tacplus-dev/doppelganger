package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// fakeAnthropic serves one canned Messages API response and keeps the request body.
func fakeAnthropic(t *testing.T, stopReason, text string) (*Anthropic, *map[string]any) {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "k" {
			t.Errorf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		data, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(data, &body); err != nil {
			t.Errorf("request body: %v", err)
		}
		resp := map[string]any{
			"id": "msg", "type": "message", "role": "assistant", "model": "m",
			"content":     []any{map[string]any{"type": "text", "text": text}},
			"stop_reason": stopReason,
			"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
		}
		if stopReason == "refusal" {
			resp["stop_details"] = map[string]any{"type": "refusal", "category": "cyber", "explanation": "no"}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	p := NewAnthropic("")
	p.client = anthropic.NewClient(option.WithAPIKey("k"), option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	return p, &body
}

func TestAnthropicRequestShape(t *testing.T) {
	p, body := fakeAnthropic(t, "end_turn", `{"kind":"ANSWER"}`)
	out, err := p.Complete(context.Background(), Request{System: "SYS", Repo: "REPO", Turn: "TURN", Schema: answerSchema})
	if err != nil || string(out) != `{"kind":"ANSWER"}` {
		t.Fatalf("out=%s err=%v", out, err)
	}
	b := *body
	if b["model"] != DefaultAnthropicModel {
		t.Errorf("model = %v", b["model"])
	}
	if _, hasTools := b["tools"]; hasTools {
		t.Error("request declares tools; the provider contract is no tools")
	}
	system := b["system"].([]any)
	if len(system) != 2 || system[0].(map[string]any)["text"] != "SYS" {
		t.Fatalf("system = %v", system)
	}
	repoBlock := system[1].(map[string]any)
	if !strings.Contains(repoBlock["text"].(string), "REPO") || repoBlock["cache_control"] == nil {
		t.Errorf("repo block not cached: %v", repoBlock)
	}
	msgs := b["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Fatalf("messages = %v", msgs)
	}
	if !strings.Contains(string(mustJSON(msgs[0])), "TURN") {
		t.Errorf("turn missing from user message: %v", msgs[0])
	}
	format := b["output_config"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["schema"].(map[string]any)["type"] != "object" {
		t.Errorf("output_config.format = %v", format)
	}
}

func TestAnthropicRefusalIsNotRetried(t *testing.T) {
	p, _ := fakeAnthropic(t, "refusal", "")
	_, err := p.Complete(context.Background(), Request{Schema: answerSchema})
	if err == nil || errors.Is(err, errStructuredOutput) || !strings.Contains(err.Error(), "cyber") {
		t.Fatalf("err = %v", err)
	}
}

func TestAnthropicInvalidJSONIsTransient(t *testing.T) {
	p, _ := fakeAnthropic(t, "end_turn", "not json")
	_, err := p.Complete(context.Background(), Request{Schema: answerSchema})
	if !errors.Is(err, errStructuredOutput) {
		t.Fatalf("err = %v", err)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
