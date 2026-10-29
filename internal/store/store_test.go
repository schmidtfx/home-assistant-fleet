package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInstancesCRUD(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	in := &Instance{ID: "a", Name: "Home", URL: "https://ha.local:8123", TokenEnc: []byte{1, 2}, Enabled: true}
	if err := s.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInstance(ctx, "a")
	if err != nil || got.Name != "Home" || !got.Enabled {
		t.Fatalf("get: %+v %v", got, err)
	}
	got.Name = "Cabin"
	got.Settings = `{"checks":{"log_errors":false}}`
	if err := s.UpdateInstance(ctx, got); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListInstances(ctx)
	if len(list) != 1 || list[0].Name != "Cabin" || list[0].Settings != `{"checks":{"log_errors":false}}` {
		t.Fatalf("list: %+v", list)
	}
	if err := s.DeleteInstance(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetInstance(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSessions(t *testing.T) {
	s, _ := OpenMemory()
	defer s.Close()
	ctx := context.Background()
	if err := s.CreateUser(ctx, "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	u, _ := s.UserByName(ctx, "admin")
	if err := s.CreateSession(ctx, "h1", u.ID, "csrf", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	sess, err := s.SessionByHash(ctx, "h1")
	if err != nil || sess.Username != "admin" || sess.Role != "admin" {
		t.Fatalf("session: %+v %v", sess, err)
	}
	if err := s.SetUserRole(ctx, "admin", "viewer"); err != nil {
		t.Fatal(err)
	}
	if sess, _ := s.SessionByHash(ctx, "h1"); sess.Role != "viewer" {
		t.Fatalf("role not updated: %+v", sess)
	}
	if err := s.SetUserRole(ctx, "nobody", "viewer"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := s.DeleteExpiredSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionByHash(ctx, "h1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected expired session to be gone, got %v", err)
	}
}

func TestSettings(t *testing.T) {
	s, _ := OpenMemory()
	defer s.Close()
	ctx := context.Background()
	if v, err := s.GetSetting(ctx, "checks"); err != nil || v != "" {
		t.Fatalf("missing setting: %q %v", v, err)
	}
	for _, want := range []string{"one", "two"} {
		if err := s.PutSetting(ctx, "checks", want); err != nil {
			t.Fatal(err)
		}
		if v, err := s.GetSetting(ctx, "checks"); err != nil || v != want {
			t.Fatalf("got %q %v, want %q", v, err, want)
		}
	}
}
