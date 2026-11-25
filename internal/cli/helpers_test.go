package cli

import (
	"testing"
	"time"

	"github.com/iamludal/kvx/internal/output"
)

func TestParseAge(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"90d": 90 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "12h": 12 * time.Hour, "0d": 0,
	} {
		got, err := parseAge(in)
		if err != nil || got != want {
			t.Errorf("parseAge(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseAge("soon"); err == nil {
		t.Error("parseAge(soon) should fail")
	}
}

func TestMatcher(t *testing.T) {
	cases := []struct {
		pattern      string
		regex, icase bool
		in           string
		want         bool
	}{
		{"pass", false, false, "PASSWORD", false},
		{"pass", false, true, "PASSWORD", true},
		{"^api_?key$", true, true, "API_KEY", true},
		{"^api_?key$", true, false, "API_KEY", false},
	}
	for _, c := range cases {
		m, err := matcher(c.pattern, c.regex, c.icase)
		if err != nil {
			t.Fatal(err)
		}
		if got := m(c.in); got != c.want {
			t.Errorf("matcher(%q, regex=%v, i=%v)(%q) = %v", c.pattern, c.regex, c.icase, c.in, got)
		}
	}
}

func TestDiffSides(t *testing.T) {
	a := &app{out: &output.Printer{ShowSecrets: true}}
	l := side{data: map[string]map[string]any{
		"db":  {"user": "app", "password": "old"},
		"old": {"k": "v"},
	}}
	r := side{data: map[string]map[string]any{
		"db":  {"user": "app", "password": "new", "host": "h"},
		"new": {"k": "v"},
	}}
	got := diffSides(a, l, r, false)
	if len(got) != 3 {
		t.Fatalf("want 3 changes, got %+v", got)
	}
	if got[0].Path != "db" || len(got[0].Keys) != 2 {
		t.Errorf("db: want host added + password changed, got %+v", got[0])
	}
	if got[1].Status != "added" || got[2].Status != "removed" {
		t.Errorf("want new added, old removed, got %+v", got[1:])
	}
	if ks := diffSides(a, l, r, true); len(ks[0].Keys) != 1 {
		t.Errorf("--keys-only should ignore the password change, got %+v", ks[0])
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("shellQuote = %s", got)
	}
}
