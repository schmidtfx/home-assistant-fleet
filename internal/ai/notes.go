package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// NotesFetcher downloads public release notes pages. Only allowlisted hosts are
// fetched (no SSRF into the local network); redirects must stay on the allowlist.
type NotesFetcher struct {
	Hosts    []string
	MaxBytes int64
	client   *http.Client
}

func NewNotesFetcher() *NotesFetcher {
	f := &NotesFetcher{Hosts: []string{"www.home-assistant.io", "home-assistant.io", "github.com"}, MaxBytes: 5 << 20}
	f.client = &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !f.allowed(req.URL) {
				return fmt.Errorf("redirect to non-allowlisted host %q", req.URL.Host)
			}
			return nil
		},
	}
	return f
}

func (f *NotesFetcher) allowed(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	for _, h := range f.Hosts {
		if strings.EqualFold(u.Host, h) {
			return true
		}
	}
	return false
}

// Fetch returns the page at rawURL converted to plain text with markdown-style headings.
func (f *NotesFetcher) Fetch(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !f.allowed(u) {
		return "", fmt.Errorf("release notes host not allowed: %q", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "hafleet (release notes review)")
	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch release notes: %s", resp.Status)
	}
	body := io.LimitReader(resp.Body, f.MaxBytes+1)
	data, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	if int64(len(data)) > f.MaxBytes {
		return "", fmt.Errorf("release notes page larger than %d bytes", f.MaxBytes)
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "html") {
		return HTMLToText(string(data)), nil
	}
	return string(data), nil
}

var skipTags = map[string]bool{"script": true, "style": true, "nav": true, "header": true, "footer": true, "noscript": true, "svg": true}
var blockTags = map[string]bool{"p": true, "div": true, "li": true, "br": true, "tr": true, "section": true, "article": true, "pre": true, "blockquote": true}

// HTMLToText converts HTML into plain text, keeping headings as "#" lines.
func HTMLToText(src string) string {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return src
	}
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if skipTags[n.Data] {
				return
			}
			if len(n.Data) == 2 && n.Data[0] == 'h' && n.Data[1] >= '1' && n.Data[1] <= '6' {
				b.WriteString("\n\n" + strings.Repeat("#", int(n.Data[1]-'0')) + " ")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				b.WriteString("\n")
				return
			}
			if n.Data == "li" {
				b.WriteString("\n- ")
			} else if blockTags[n.Data] {
				b.WriteString("\n")
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(strings.Join(strings.Fields(n.Data), " ") + " ")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	// Collapse blank-line runs.
	lines := strings.Split(b.String(), "\n")
	var out []string
	blank := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// BreakingSection extracts the "Backward-incompatible changes" / "Breaking
// changes" section from markdown-style text. It returns the input unchanged
// (and false) when no such heading exists.
func BreakingSection(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	start, level := -1, 0
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "#") {
			continue
		}
		lvl := len(t) - len(strings.TrimLeft(t, "#"))
		heading := strings.ToLower(t)
		if start < 0 {
			if strings.Contains(heading, "backward-incompatible") || strings.Contains(heading, "backwards-incompatible") ||
				strings.Contains(heading, "breaking change") {
				start, level = i, lvl
			}
			continue
		}
		if lvl <= level {
			return strings.Join(lines[start:i], "\n"), true
		}
	}
	if start < 0 {
		return text, false
	}
	return strings.Join(lines[start:], "\n"), true
}
