package ai

import (
	"context"
	"encoding/json"
	"sync"
)

// Fake is a scripted provider for tests. Reply is called for every request;
// all requests are recorded so tests can inspect what was sent.
type Fake struct {
	Reply func(req Request) (*Response, error)

	mu       sync.Mutex
	requests []Request
}

func (f *Fake) Name() string  { return "fake" }
func (f *Fake) Model() string { return "fake-model" }

func (f *Fake) Complete(_ context.Context, req Request, onText func(string)) (*Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	resp, err := f.Reply(req)
	if err != nil {
		return nil, err
	}
	if onText != nil && resp.Text != "" && req.Schema == nil {
		onText(resp.Text)
	}
	resp.Message = Message{Role: RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls}
	return resp, nil
}

// Requests returns all requests received so far.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

// JSONReply returns a response whose text is v encoded as JSON.
func JSONReply(v any) *Response {
	b, _ := json.Marshal(v)
	return &Response{Text: string(b)}
}
