package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type OllamaOptions struct {
	URL    string // default http://localhost:11434
	Model  string // required
	NumCtx int    // context window to request; also used for the length check
}

// Ollama is a local-model backend using Ollama's /api/chat endpoint.
type Ollama struct {
	url    string
	model  string
	numCtx int
	http   *http.Client
}

func NewOllama(o OllamaOptions) (*Ollama, error) {
	if o.Model == "" {
		return nil, errors.New("ollama: a model must be configured (HAFLEET_OLLAMA_MODEL)")
	}
	if o.URL == "" {
		o.URL = "http://localhost:11434"
	}
	if o.NumCtx == 0 {
		o.NumCtx = 32768
	}
	return &Ollama{url: strings.TrimRight(o.URL, "/"), model: o.Model, numCtx: o.NumCtx,
		http: &http.Client{Timeout: 10 * time.Minute}}, nil
}

func (o *Ollama) Name() string  { return "ollama" }
func (o *Ollama) Model() string { return o.model }

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
}

type ollamaToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type ollamaChunk struct {
	Message         ollamaMessage `json:"message"`
	Done            bool          `json:"done"`
	DoneReason      string        `json:"done_reason"`
	PromptEvalCount int64         `json:"prompt_eval_count"`
	EvalCount       int64         `json:"eval_count"`
	Error           string        `json:"error"`
}

func (o *Ollama) Complete(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	msgs := []ollamaMessage{{Role: "system", Content: req.System}}
	chars := len(req.System)
	for _, m := range req.Messages {
		for _, r := range m.ToolResults {
			msgs = append(msgs, ollamaMessage{Role: "tool", Content: r.Content, ToolName: r.Name})
			chars += len(r.Content)
		}
		if m.Role == RoleAssistant {
			am := ollamaMessage{Role: "assistant", Content: m.Text}
			for _, c := range m.ToolCalls {
				var tc ollamaToolCall
				tc.Function.Name = c.Name
				tc.Function.Arguments = c.Input
				am.ToolCalls = append(am.ToolCalls, tc)
			}
			msgs = append(msgs, am)
		} else if m.Text != "" {
			msgs = append(msgs, ollamaMessage{Role: "user", Content: m.Text})
		}
		chars += len(m.Text)
	}
	// Rough estimate (~4 chars per token): refuse instead of silently truncating.
	if chars/4 > o.numCtx*9/10 {
		return nil, fmt.Errorf("%w (about %d tokens, context is %d)", ErrTooLong, chars/4, o.numCtx)
	}

	body := map[string]any{
		"model": o.model, "messages": msgs, "stream": true,
		"options": map[string]any{"num_ctx": o.numCtx},
	}
	if req.Schema != nil {
		body["format"] = req.Schema
	}
	if len(req.Tools) > 0 {
		var tools []any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Schema,
			}})
		}
		body["tools"] = tools
	}
	data, _ := json.Marshal(body)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url+"/api/chat", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hresp, err := o.http.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	defer hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(hresp.Body, 4096))
		return nil, fmt.Errorf("ollama: %s: %s", hresp.Status, strings.TrimSpace(string(b)))
	}

	resp := &Response{}
	var text strings.Builder
	sc := bufio.NewScanner(hresp.Body)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	for sc.Scan() {
		var ch ollamaChunk
		if err := json.Unmarshal(sc.Bytes(), &ch); err != nil {
			return nil, fmt.Errorf("ollama: bad stream chunk: %w", err)
		}
		if ch.Error != "" {
			return nil, fmt.Errorf("ollama: %s", ch.Error)
		}
		if ch.Message.Content != "" {
			text.WriteString(ch.Message.Content)
			if onText != nil && req.Schema == nil {
				onText(ch.Message.Content)
			}
		}
		for _, tc := range ch.Message.ToolCalls {
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID: fmt.Sprintf("call_%d", len(resp.ToolCalls)+1), Name: tc.Function.Name, Input: tc.Function.Arguments,
			})
		}
		if ch.Done {
			resp.Usage = Usage{InputTokens: ch.PromptEvalCount, OutputTokens: ch.EvalCount}
			if ch.DoneReason == "length" {
				return nil, errors.New("ollama: the response was cut off (context or output limit reached)")
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	resp.Text = text.String()
	resp.Message = Message{Role: RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls}
	return resp, nil
}
