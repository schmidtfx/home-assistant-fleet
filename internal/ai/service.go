package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/auth"
	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

// promptVersion is part of every cache key; bump it when prompts change.
const promptVersion = "v3"

type Options struct {
	Effort     string // default effort for all features
	ChatLimit  int    // AI requests per user per hour
	AutoReview bool   // review new update tasks in the background
	Logger     *slog.Logger
}

// Service implements the AI features. A nil *Service means AI is disabled.
type Service struct {
	p       Provider
	store   *store.Store
	fleet   *fleet.Manager
	notes   *NotesFetcher
	opts    Options
	log     *slog.Logger
	limiter *auth.RateLimiter

	inflight sync.Map // cache key -> *sync.Mutex (dedupe concurrent generations)

	convMu sync.Mutex
	convs  map[string]*conversation

	briefMu  sync.Mutex
	briefing *Briefing

	usageMu sync.Mutex
	usage   map[string]FeatureUsage // by feature
}

// FeatureUsage adds up model calls and tokens for one feature (for metrics).
type FeatureUsage struct {
	Requests int64
	Usage
}

func NewService(p Provider, s *store.Store, f *fleet.Manager, opts Options) *Service {
	if opts.Effort == "" {
		opts.Effort = "medium"
	}
	if opts.ChatLimit <= 0 {
		opts.ChatLimit = 60
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Service{
		p: p, store: s, fleet: f, notes: NewNotesFetcher(), opts: opts, log: opts.Logger,
		limiter: auth.NewRateLimiter(opts.ChatLimit, time.Hour),
		convs:   map[string]*conversation{},
		usage:   map[string]FeatureUsage{},
	}
}

// UsageTotals returns model calls and tokens per feature since start. Nil-safe.
func (s *Service) UsageTotals() map[string]FeatureUsage {
	if s == nil {
		return nil
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	out := make(map[string]FeatureUsage, len(s.usage))
	for k, v := range s.usage {
		out[k] = v
	}
	return out
}

type Status struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

func (s *Service) Status() Status {
	if s == nil {
		return Status{}
	}
	return Status{Enabled: true, Provider: s.p.Name(), Model: s.p.Model()}
}

var ErrRateLimited = errors.New("AI request limit reached, try again later")

// allow enforces the per-user hourly budget for model calls.
func (s *Service) allow(user string) error {
	key := "ai|" + user
	if !s.limiter.Allow(key) {
		return ErrRateLimited
	}
	s.limiter.Fail(key) // RateLimiter counts "failures"; here every request counts
	return nil
}

// view returns an instance view, requiring AI to be enabled for it.
func (s *Service) view(instanceID string) (fleet.InstanceView, error) {
	v, ok := s.fleet.View(instanceID)
	if !ok {
		return v, fmt.Errorf("instance not found")
	}
	if !v.AIEnabled {
		return v, fmt.Errorf("%w for instance %s", ErrDisabled, v.Name)
	}
	return v, nil
}

func (s *Service) task(v fleet.InstanceView, taskID string) (tasks.Task, error) {
	for _, t := range v.Tasks {
		if t.ID == taskID {
			return t, nil
		}
	}
	return tasks.Task{}, fmt.Errorf("task not found")
}

func cacheKey(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])
}

// cached returns a cached result for key, or runs gen (once per key at a time) and stores it.
func (s *Service) cached(ctx context.Context, key, kind string, refresh bool, out any, gen func() (any, error)) error {
	mu, _ := s.inflight.LoadOrStore(key, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if !refresh {
		if e, err := s.store.GetAICache(ctx, key); err == nil {
			return json.Unmarshal([]byte(e.Result), out)
		}
	}
	v, err := gen()
	if err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := s.store.PutAICache(ctx, key, &store.AICacheEntry{Kind: kind, Result: string(data), Provider: s.p.Name(), Model: s.p.Model()}); err != nil {
		s.log.Warn("store ai cache", "err", err)
	}
	return json.Unmarshal(data, out)
}

func (s *Service) logUsage(feature string, u Usage) {
	s.usageMu.Lock()
	t := s.usage[feature]
	t.Requests++
	t.InputTokens += u.InputTokens
	t.OutputTokens += u.OutputTokens
	t.CacheReadTokens += u.CacheReadTokens
	t.CacheWriteTokens += u.CacheWriteTokens
	s.usage[feature] = t
	s.usageMu.Unlock()
	s.log.Info("ai usage", "feature", feature, "provider", s.p.Name(), "model", s.p.Model(),
		"input_tokens", u.InputTokens, "output_tokens", u.OutputTokens,
		"cache_read_tokens", u.CacheReadTokens, "cache_write_tokens", u.CacheWriteTokens)
}

// Run starts background maintenance (cache pruning, conversation expiry, auto-review).
func (s *Service) Run(ctx context.Context) {
	if s.opts.AutoReview {
		go s.autoReview(ctx)
	}
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		_ = s.store.PruneAICache(ctx, 90*24*time.Hour)
		s.expireConversations()
		s.limiter.Prune()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func jsonString(v any) string {
	b, _ := json.MarshalIndent(v, "", " ")
	return string(b)
}
