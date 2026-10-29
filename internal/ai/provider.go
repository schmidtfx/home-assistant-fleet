// Package ai adds advisory AI features (update risk review, task explainer,
// fleet briefing, chat) on top of the fleet manager. Models never receive HA
// tokens and never execute actions: they only read redacted fleet data and can
// propose actions that a user must confirm.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Provider is one model backend. Complete performs a single model turn; tool
// loops and feature logic live in this package so all backends behave alike.
type Provider interface {
	Name() string
	Model() string
	Complete(ctx context.Context, req Request, onText func(string)) (*Response, error)
}

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is a provider-neutral conversation turn.
type Message struct {
	Role        Role
	Text        string
	ToolCalls   []ToolCall   // assistant turns
	ToolResults []ToolResult // user turns answering tool calls
	// Native holds the provider's own representation of an assistant turn
	// (e.g. Claude content including thinking blocks), replayed unchanged.
	Native any
}

type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

type ToolResult struct {
	CallID  string
	Name    string
	Content string
	IsError bool
}

// ToolDef describes a tool. Schema is a JSON-schema object
// ({"type":"object","properties":...,"required":[...]}).
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any
}

type Request struct {
	System    string // stable instructions; providers may cache it
	Messages  []Message
	Tools     []ToolDef
	Schema    map[string]any // if set, the reply must be JSON matching this schema
	Effort    string         // low|medium|high (Claude only)
	MaxTokens int
}

type Usage struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int64
}

type Response struct {
	Text      string
	ToolCalls []ToolCall
	Message   Message // the assistant turn to append to history
	Usage     Usage
}

var (
	// ErrRefused is returned when the model declines to answer.
	ErrRefused = errors.New("the model declined to answer this request")
	// ErrTooLong is returned when the input does not fit the model's context.
	ErrTooLong = errors.New("input is too long for the configured model")
	// ErrDisabled is returned when AI is off globally or for an instance.
	ErrDisabled = errors.New("AI features are disabled")
)

// CompleteJSON runs a structured-output request and decodes the reply into out.
func CompleteJSON(ctx context.Context, p Provider, req Request, out any) (Usage, error) {
	if req.Schema == nil {
		return Usage{}, errors.New("CompleteJSON requires a schema")
	}
	resp, err := p.Complete(ctx, req, nil)
	if err != nil {
		return Usage{}, err
	}
	if err := json.Unmarshal([]byte(resp.Text), out); err != nil {
		return resp.Usage, fmt.Errorf("model returned invalid JSON: %w", err)
	}
	return resp.Usage, nil
}

// obj is a small helper for writing JSON schemas.
func obj(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func enum(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": desc}
}

func arr(items map[string]any) map[string]any { return map[string]any{"type": "array", "items": items} }
