package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sse renders Anthropic streaming events.
func sse(events ...map[string]any) string {
	var b strings.Builder
	for _, ev := range events {
		data, _ := json.Marshal(ev)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", ev["type"], data)
	}
	return b.String()
}

func claudeStream(stopReason string, blocks ...map[string]any) string {
	evs := []map[string]any{{"type": "message_start", "message": map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5", "content": []any{},
		"stop_reason": nil, "usage": map[string]any{"input_tokens": 100, "output_tokens": 0, "cache_read_input_tokens": 80},
	}}}
	for i, b := range blocks {
		switch b["type"] {
		case "text":
			evs = append(evs,
				map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "text", "text": ""}},
				map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "text_delta", "text": b["text"]}})
		case "tool_use":
			evs = append(evs,
				map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{
					"type": "tool_use", "id": b["id"], "name": b["name"], "input": map[string]any{}}},
				map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{
					"type": "input_json_delta", "partial_json": b["input"]}})
		}
		evs = append(evs, map[string]any{"type": "content_block_stop", "index": i})
	}
	delta := map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason}, "usage": map[string]any{"output_tokens": 7}}
	if stopReason == "refusal" {
		delta["delta"].(map[string]any)["stop_details"] = map[string]any{"type": "refusal", "category": "cyber", "explanation": "no"}
	}
	evs = append(evs, delta, map[string]any{"type": "message_stop"})
	return sse(evs...)
}

type captured struct {
	body    map[string]any
	headers http.Header
}

func fakeClaude(t *testing.T, responses ...string) (*Anthropic, *[]captured) {
	var reqs []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(data, &body)
		reqs = append(reqs, captured{body: body, headers: r.Header.Clone()})
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, responses[min(len(reqs)-1, len(responses)-1)])
	}))
	t.Cleanup(srv.Close)
	return NewAnthropic(AnthropicOptions{BaseURL: srv.URL, APIKey: "test", Fallbacks: true}), &reqs
}

func TestAnthropicStructuredRequest(t *testing.T) {
	p, reqs := fakeClaude(t, claudeStream("end_turn", map[string]any{"type": "text", "text": `{"risk":"low"}`}))
	var out struct{ Risk string }
	usage, err := CompleteJSON(context.Background(), p, Request{
		System: "sys", Schema: obj(map[string]any{"risk": str("r")}, "risk"), Effort: "medium",
		Messages: []Message{{Role: RoleUser, Text: "hello"}},
	}, &out)
	if err != nil || out.Risk != "low" {
		t.Fatalf("got %+v %v", out, err)
	}
	if usage.CacheReadTokens != 80 || usage.OutputTokens != 7 {
		t.Errorf("usage: %+v", usage)
	}
	b := (*reqs)[0].body
	if b["model"] != "claude-opus-5-5" || b["stream"] != true {
		t.Errorf("model/stream: %v %v", b["model"], b["stream"])
	}
	oc, _ := b["output_config"].(map[string]any)
	if oc["effort"] != "medium" || oc["format"] == nil {
		t.Errorf("output_config: %+v", oc)
	}
	sys := b["system"].([]any)[0].(map[string]any)
	if sys["cache_control"] == nil {
		t.Error("system prompt must carry cache_control")
	}
	if _, ok := b["thinking"]; ok {
		t.Error("thinking must be left unset for Opus 5.5")
	}
	if b["fallbacks"] != "default" || !strings.Contains((*reqs)[0].headers.Get("anthropic-beta"), "server-side-fallback-2026-07-01") {
		t.Errorf("fallbacks: %v / %q", b["fallbacks"], (*reqs)[0].headers.Get("anthropic-beta"))
	}
}

func TestAnthropicToolUseAndReplay(t *testing.T) {
	p, reqs := fakeClaude(t,
		claudeStream("tool_use",
			map[string]any{"type": "text", "text": "Checking."},
			map[string]any{"type": "tool_use", "id": "toolu_1", "name": "list_instances", "input": `{}`}),
		claudeStream("end_turn", map[string]any{"type": "text", "text": "Done."}),
	)
	tools := []ToolDef{{Name: "list_instances", Description: "d", Schema: obj(map[string]any{})}}
	var streamed strings.Builder
	msgs := []Message{{Role: RoleUser, Text: "hi"}}
	r1, err := p.Complete(context.Background(), Request{System: "s", Tools: tools, Messages: msgs}, func(s string) { streamed.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.ToolCalls) != 1 || r1.ToolCalls[0].Name != "list_instances" || streamed.String() != "Checking." {
		t.Fatalf("tool calls %+v streamed %q", r1.ToolCalls, streamed.String())
	}
	tool := (*reqs)[0].body["tools"].([]any)[0].(map[string]any)
	if tool["strict"] != true || tool["input_schema"].(map[string]any)["additionalProperties"] != false {
		t.Errorf("tool def: %+v", tool)
	}
	if _, ok := (*reqs)[0].body["tool_choice"]; ok {
		t.Error("tool_choice must not be forced")
	}

	msgs = append(msgs, r1.Message, Message{Role: RoleUser, ToolResults: []ToolResult{{CallID: "toolu_1", Content: "[]"}}})
	if _, err := p.Complete(context.Background(), Request{System: "s", Tools: tools, Messages: msgs}, nil); err != nil {
		t.Fatal(err)
	}
	sent := (*reqs)[1].body["messages"].([]any)
	asst := sent[1].(map[string]any)
	if asst["role"] != "assistant" || len(asst["content"].([]any)) != 2 {
		t.Errorf("assistant turn not replayed natively: %+v", asst)
	}
	res := sent[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if res["type"] != "tool_result" || res["tool_use_id"] != "toolu_1" {
		t.Errorf("tool result: %+v", res)
	}
}

func TestAnthropicRefusal(t *testing.T) {
	p, _ := fakeClaude(t, claudeStream("refusal"))
	_, err := p.Complete(context.Background(), Request{System: "s", Messages: []Message{{Role: RoleUser, Text: "x"}}}, nil)
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "cyber") {
		t.Fatalf("expected refusal, got %v", err)
	}
}

func TestOllama(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"message":{"role":"assistant","content":"Look"}}`+"\n")
		io.WriteString(w, `{"message":{"role":"assistant","content":"ing","tool_calls":[{"function":{"name":"list_tasks","arguments":{"kind":"update"}}}]}}`+"\n")
		io.WriteString(w, `{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":42,"eval_count":5}`+"\n")
	}))
	defer srv.Close()
	p, err := NewOllama(OllamaOptions{URL: srv.URL, Model: "qwen", NumCtx: 4096})
	if err != nil {
		t.Fatal(err)
	}
	var streamed string
	resp, err := p.Complete(context.Background(), Request{
		System: "sys", Tools: []ToolDef{{Name: "list_tasks", Schema: obj(map[string]any{})}},
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
	}, func(s string) { streamed += s })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Looking" || streamed != "Looking" || len(resp.ToolCalls) != 1 || string(resp.ToolCalls[0].Input) != `{"kind":"update"}` {
		t.Fatalf("resp %+v streamed %q", resp, streamed)
	}
	if resp.Usage.InputTokens != 42 {
		t.Errorf("usage %+v", resp.Usage)
	}
	msgs := got["messages"].([]any)
	if got["model"] != "qwen" || msgs[0].(map[string]any)["role"] != "system" || got["tools"] == nil {
		t.Errorf("request: %+v", got)
	}

	if _, err := p.Complete(context.Background(), Request{System: "s", Schema: obj(map[string]any{}),
		Messages: []Message{{Role: RoleUser, Text: strings.Repeat("x", 4096*4)}}}, nil); !errors.Is(err, ErrTooLong) {
		t.Fatalf("expected ErrTooLong, got %v", err)
	}
	if _, err := NewOllama(OllamaOptions{}); err == nil {
		t.Fatal("model must be required")
	}
}
