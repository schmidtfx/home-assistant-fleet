package actions

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
	"github.com/schmidtfx/home-assistant-fleet/internal/hass/hasstest"
	"github.com/schmidtfx/home-assistant-fleet/internal/secrets"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

func TestInstallUpdate(t *testing.T) {
	ha := hasstest.New("tok")
	defer ha.Close()
	ha.Result("get_config", map[string]any{"version": "2026.10.1", "components": []string{}})
	ha.Result("auth/current_user", map[string]any{"is_admin": true})
	ha.Result("get_states", []any{map[string]any{"entity_id": "update.demo", "state": "on", "attributes": map[string]any{
		"title": "Demo", "installed_version": "1", "latest_version": "2", "supported_features": 1 | 8}}})
	ha.Result("repairs/list_issues", map[string]any{"issues": []any{}})
	ha.Result("config_entries/get", []any{})
	ha.Result("config_entries/flow/progress", []any{})
	ha.Result("call_service", map[string]any{})

	st, _ := store.OpenMemory()
	defer st.Close()
	box, _ := secrets.New(bytes.Repeat([]byte{1}, 32))
	enc, _ := box.Seal([]byte("tok"), []byte("i1"))
	ctx := context.Background()
	st.CreateInstance(ctx, &store.Instance{ID: "i1", Name: "Home", URL: ha.URL, TokenEnc: enc, AllowInsecureHTTP: true, Enabled: true})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := fleet.New(st, box, fleet.Options{Logger: log})
	mctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.Start(mctx)

	var task tasks.Task
	for i := 0; i < 250; i++ {
		if v, _ := m.View("i1"); len(v.Tasks) == 1 && v.Status.State == fleet.StateConnected {
			task = v.Tasks[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if task.ID == "" {
		t.Fatal("update task never appeared")
	}

	r := NewRunner(m, st, log)
	if _, err := r.Submit("admin", "i1", Request{TaskID: task.ID, Action: tasks.ActionRebootHost}); !errors.Is(err, ErrActionNotAllowed) {
		t.Fatalf("expected ErrActionNotAllowed, got %v", err)
	}
	if _, err := r.Submit("admin", "i1", Request{TaskID: "nope", Action: tasks.ActionInstallUpdate}); !errors.Is(err, ErrUnknownTask) {
		t.Fatalf("expected ErrUnknownTask, got %v", err)
	}
	rec, err := r.Submit("admin", "i1", Request{TaskID: task.ID, Action: tasks.ActionInstallUpdate, Backup: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusRunning {
		t.Fatalf("status %s", rec.Status)
	}
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	done, err := r.Wait(wctx, rec.ID)
	if err != nil || done.Status != StatusSucceeded || done.FinishedAt.IsZero() {
		t.Fatalf("wait: %+v %v", done, err)
	}
	if again, err := r.Wait(wctx, rec.ID); err != nil || again.Status != StatusSucceeded {
		t.Fatalf("wait after finish: %+v %v", again, err)
	}
	if _, err := r.Wait(wctx, "nope"); !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("wait unknown: %v", err)
	}

	var final Record
	for i := 0; i < 250; i++ {
		if recs := r.Recent(); len(recs) == 1 && recs[0].Status != StatusRunning {
			final = recs[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Status != StatusSucceeded {
		t.Fatalf("action did not succeed: %+v", final)
	}

	var call map[string]any
	for _, c := range ha.Calls() {
		if c["type"] == "call_service" {
			call = c
		}
	}
	data, _ := call["service_data"].(map[string]any)
	if call["domain"] != "update" || call["service"] != "install" || data["entity_id"] != "update.demo" || data["backup"] != true {
		t.Fatalf("unexpected service call: %+v", call)
	}

	audit, _ := st.ListAudit(ctx, 10)
	if len(audit) != 2 || audit[0].Result != "succeeded" || audit[1].Result != "submitted" {
		t.Fatalf("unexpected audit: %+v", audit)
	}
}
