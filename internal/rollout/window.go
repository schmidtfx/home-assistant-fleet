package rollout

import (
	"fmt"
	"strings"
	"time"
)

// Window is a daily maintenance window in server local time, such as
// "02:00-05:00". It may wrap past midnight ("23:00-02:00"). The zero Window
// (from "") allows any time.
type Window struct {
	start, end int // minutes after midnight
	set        bool
}

// ParseWindow parses "HH:MM-HH:MM"; an empty string means any time.
func ParseWindow(s string) (Window, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Window{}, nil
	}
	from, to, ok := strings.Cut(s, "-")
	if !ok {
		return Window{}, fmt.Errorf("maintenance window %q: use HH:MM-HH:MM", s)
	}
	start, err1 := parseClock(strings.TrimSpace(from))
	end, err2 := parseClock(strings.TrimSpace(to))
	if err1 != nil || err2 != nil {
		return Window{}, fmt.Errorf("maintenance window %q: use HH:MM-HH:MM", s)
	}
	if start == end {
		return Window{}, fmt.Errorf("maintenance window %q: start and end must differ", s)
	}
	return Window{start: start, end: end, set: true}, nil
}

func parseClock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, err
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Contains reports whether t (in its own location) is inside the window.
func (w Window) Contains(t time.Time) bool {
	if !w.set {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if w.start < w.end {
		return m >= w.start && m < w.end
	}
	return m >= w.start || m < w.end // wraps past midnight
}

func (w Window) String() string {
	if !w.set {
		return ""
	}
	return fmt.Sprintf("%02d:%02d-%02d:%02d", w.start/60, w.start%60, w.end/60, w.end%60)
}
