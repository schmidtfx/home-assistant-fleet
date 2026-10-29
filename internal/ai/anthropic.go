package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/anthropics/anthropic-sdk-go/v2/shared/constant"
)

const DefaultClaudeModel = "claude-opus-5-5"

type AnthropicOptions struct {
	Model     string // default claude-opus-5-5
	Fallbacks bool   // server-side refusal fallbacks ("default" routing)
	BaseURL   string // for tests
	APIKey    string // optional; otherwise ANTHROPIC_API_KEY / ant auth profile
}

// Anthropic is the Claude backend. It uses the beta Messages endpoint because
// server-side refusal fallbacks are only available there.
type Anthropic struct {
	client    anthropic.Client
	model     string
	fallbacks bool
}

func NewAnthropic(o AnthropicOptions) *Anthropic {
	var opts []option.RequestOption
	if o.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(o.BaseURL))
	}
	if o.APIKey != "" {
		opts = append(opts, option.WithAPIKey(o.APIKey))
	}
	if o.Model == "" {
		o.Model = DefaultClaudeModel
	}
	return &Anthropic{client: anthropic.NewClient(opts...), model: o.Model, fallbacks: o.Fallbacks}
}

func (a *Anthropic) Name() string  { return "anthropic" }
func (a *Anthropic) Model() string { return a.model }

func (a *Anthropic) Complete(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 32000 // always streamed, so a generous cap does not risk HTTP timeouts
	}
	params := anthropic.BetaMessageNewParams{
		Model:     a.model,
		MaxTokens: int64(maxTokens),
		// Thinking is left unset: it is always on (adaptive) for Claude Opus 5.5.
		System: []anthropic.BetaTextBlockParam{{
			Text:         req.System,
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(), // caches tools + system prefix
		}},
		Messages: toClaudeMessages(req.Messages),
	}
	if req.Effort != "" {
		params.OutputConfig.Effort = anthropic.BetaOutputConfigEffort(req.Effort)
	}
	if req.Schema != nil {
		params.OutputConfig.Format = anthropic.BetaJSONOutputFormatParam{Schema: req.Schema}
	}
	for _, t := range req.Tools {
		params.Tools = append(params.Tools, anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.BetaToolInputSchemaParam{
				Properties:  t.Schema["properties"],
				Required:    toStrings(t.Schema["required"]),
				ExtraFields: map[string]any{"additionalProperties": false},
			},
			Strict:              anthropic.Bool(true),
			EagerInputStreaming: anthropic.Bool(true), // inputs are validated by each tool handler
		}})
	}
	if a.fallbacks {
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
		params.Betas = append(params.Betas, anthropic.AnthropicBetaServerSideFallback2026_07_01)
	}

	stream := a.client.Beta.Messages.NewStreaming(ctx, params)
	msg := anthropic.BetaMessage{}
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return nil, fmt.Errorf("claude stream: %w", err)
		}
		if onText != nil {
			if d, ok := ev.AsAny().(anthropic.BetaRawContentBlockDeltaEvent); ok {
				if t, ok := d.Delta.AsAny().(anthropic.BetaTextDelta); ok {
					onText(t.Text)
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, claudeError(err)
	}

	switch msg.StopReason {
	case anthropic.BetaStopReasonRefusal:
		if c := string(msg.StopDetails.Category); c != "" {
			return nil, fmt.Errorf("%w (category: %s)", ErrRefused, c)
		}
		return nil, ErrRefused
	case anthropic.BetaStopReasonMaxTokens:
		return nil, errors.New("the model's response was cut off (max_tokens reached)")
	}

	resp := &Response{Usage: Usage{
		InputTokens:      msg.Usage.InputTokens,
		OutputTokens:     msg.Usage.OutputTokens,
		CacheReadTokens:  msg.Usage.CacheReadInputTokens,
		CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
	}}
	var text strings.Builder
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.BetaTextBlock:
			text.WriteString(b.Text)
		case anthropic.BetaToolUseBlock:
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Input: []byte(b.JSON.Input.Raw())})
		}
	}
	resp.Text = text.String()
	resp.Message = Message{Role: RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls, Native: msg.ToParam()}
	return resp, nil
}

func toClaudeMessages(msgs []Message) []anthropic.BetaMessageParam {
	out := make([]anthropic.BetaMessageParam, 0, len(msgs))
	for _, m := range msgs {
		if native, ok := m.Native.(anthropic.BetaMessageParam); ok {
			out = append(out, native) // replay unchanged (keeps thinking blocks valid)
			continue
		}
		var blocks []anthropic.BetaContentBlockParamUnion
		for _, r := range m.ToolResults {
			blocks = append(blocks, anthropic.NewBetaToolResultBlock(r.CallID, r.Content, r.IsError))
		}
		if m.Text != "" {
			blocks = append(blocks, anthropic.NewBetaTextBlock(m.Text))
		}
		if m.Role == RoleAssistant {
			out = append(out, anthropic.BetaMessageParam{OfAssistant: &anthropic.BetaAssistantMessageParam{Content: blocks}})
		} else {
			out = append(out, anthropic.NewBetaUserMessage(blocks...))
		}
	}
	return out
}

func claudeError(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == 401 || apiErr.StatusCode == 403:
			return fmt.Errorf("claude api: authentication failed (check ANTHROPIC_API_KEY): %w", err)
		case apiErr.StatusCode == 429:
			return fmt.Errorf("claude api: rate limited, try again shortly: %w", err)
		case apiErr.StatusCode >= 500:
			return fmt.Errorf("claude api: service error: %w", err)
		}
		return fmt.Errorf("claude api: request rejected (%d): %w", apiErr.StatusCode, err)
	}
	return fmt.Errorf("claude api: %w", err)
}

func toStrings(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, x := range s {
			if str, ok := x.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}
