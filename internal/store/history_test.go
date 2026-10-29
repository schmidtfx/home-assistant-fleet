package store

import (
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	loc := time.FixedZone("test", 3600)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, loc)
	at := func(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, loc) }
	ptr := func(t time.Time) *time.Time { return &t }
	entries := []HistoryEntry{
		// Opened before the window, closed inside it.
		{InstanceID: "a", TaskID: "t1", Kind: "repair", Severity: "warning", Title: "R", OpenedAt: at(20, 0), ClosedAt: ptr(at(27, 0))},
		// Recurs three times; the last one is still open.
		{InstanceID: "a", TaskID: "t2", Kind: "setup_failed", Severity: "warning", Title: "old", OpenedAt: at(27, 1), ClosedAt: ptr(at(27, 2))},
		{InstanceID: "a", TaskID: "t2", Kind: "setup_failed", Severity: "warning", Title: "old", OpenedAt: at(28, 1), ClosedAt: ptr(at(28, 5))},
		{InstanceID: "a", TaskID: "t2", Kind: "setup_failed", Severity: "critical", Title: "new", OpenedAt: at(29, 1)},
	}
	s := Summarize(entries, 3, now)
	if len(s.Days) != 3 || s.Days[0].Date != "2026-10-27" {
		t.Fatalf("days: %+v", s.Days)
	}
	// Oct 27: t1 closed at 00:00, t2 opened and closed. Nothing open at the end of the day.
	if d := s.Days[0]; d.Opened != 1 || d.Closed != 2 || d.Open != 0 {
		t.Fatalf("day 27: %+v", d)
	}
	if d := s.Days[2]; d.Opened != 1 || d.Open != 1 || d.OpenBySeverity["critical"] != 1 {
		t.Fatalf("today: %+v", d)
	}
	var sf KindStats
	for _, k := range s.Kinds {
		if k.Kind == "setup_failed" {
			sf = k
		}
	}
	if sf.Opened != 3 || sf.Resolved != 2 || sf.MeanSeconds != 2.5*3600 || sf.MedianSeconds != 2.5*3600 {
		t.Fatalf("setup_failed stats: %+v", sf)
	}
	if len(s.Recurring) != 1 || s.Recurring[0].Count != 3 || s.Recurring[0].Title != "new" {
		t.Fatalf("recurring: %+v", s.Recurring)
	}
}
