package cli

import (
	"bytes"
	"strings"
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

func TestHumanAge(t *testing.T) {
	now := time.Now()
	for d, want := range map[time.Duration]string{
		10 * time.Second:         "10s",
		5 * time.Minute:          "5m",
		30 * time.Hour:           "30h",
		10 * 24 * time.Hour:      "10d",
		3 * 365 * 24 * time.Hour: "3y",
	} {
		if got := humanAge(now.Add(-d)); got != want {
			t.Errorf("humanAge(-%v) = %s, want %s", d, got, want)
		}
	}
}

func TestSmallHelpers(t *testing.T) {
	if dirsLabel(1) != "1 directory" || dirsLabel(2) != "2 directories" {
		t.Error("dirsLabel")
	}
	if relUnder("", "a/b") != "a/b" || relUnder("a", "a/b") != "b" {
		t.Error("relUnder")
	}
	if joinPath("", "x") != "x" || joinPath("a/", "b") != "a/b" {
		t.Error("joinPath")
	}
	if suffix(" = ", "") != "" || suffix(" = ", "v") != " = v" {
		t.Error("suffix")
	}
	if orDash("") != "-" || orDefault("", "d") != "d" || orDefault("x", "d") != "x" {
		t.Error("orDash/orDefault")
	}
	if !isEmpty(map[string]any{"a": " ", "b": nil}) || isEmpty(map[string]any{"a": "x"}) {
		t.Error("isEmpty")
	}
	if got := describeKeyChanges(map[string]any{"a": 1, "b": 2}, map[string]any{"a": 1, "b": 3, "c": 4}); got != " (+1 ~1 keys)" {
		t.Errorf("describeKeyChanges = %q", got)
	}
	if describeKeyChanges(map[string]any{"a": 1}, map[string]any{"a": 1}) != "" {
		t.Error("describeKeyChanges with no change")
	}
}

func TestRunExitCodes(t *testing.T) {
	var out, errW bytes.Buffer
	if code := Run([]string{"--help"}, &out, &errW); code != 0 || !strings.Contains(out.String(), "Explore:") {
		t.Errorf("--help: code %d\n%s", code, out.String())
	}
	out.Reset()
	if code := Run([]string{"ls", "secret/", "-o", "xml"}, &out, &errW); code != 2 || !strings.Contains(errW.String(), "kvx: unknown output format") {
		t.Errorf("bad flag value: code %d, stderr %q", code, errW.String())
	}
	errW.Reset()
	if code := Run([]string{"nope"}, &out, &errW); code != 2 || !strings.Contains(errW.String(), "unknown command") {
		t.Errorf("unknown command: code %d, stderr %q", code, errW.String())
	}
}

func TestReplacer(t *testing.T) {
	data := map[string]any{
		"url": "redis://OLD:1", "port": 5432, "ok": true,
		"OLD_KEY": "x", "nested": map[string]any{"h": "old", "OLD2": "old"}, "list": []any{"old", 1},
	}
	r, _ := newReplacer("old", "new", false, true)

	rw := r.rewriteData(data, false, true)
	if rw.data["url"] != "redis://new:1" || rw.data["port"] != 5432 || rw.data["ok"] != true {
		t.Errorf("values: %v", rw.data)
	}
	if rw.data["nested"].(map[string]any)["h"] != "new" || rw.data["list"].([]any)[0] != "new" {
		t.Errorf("nested: %v", rw.data)
	}
	if _, ok := rw.data["OLD_KEY"]; !ok || data["url"] != "redis://OLD:1" {
		t.Error("keys must stay, input must not be modified")
	}

	rw = r.rewriteData(data, true, false)
	if _, ok := rw.data["new_KEY"]; !ok || rw.data["url"] != "redis://OLD:1" || len(rw.changes) != 2 {
		t.Errorf("keys: %v / %+v", rw.data, rw.changes)
	}

	rw = r.rewriteData(map[string]any{"a_old": 1, "a_new": 2}, true, false)
	if len(rw.collisions) != 1 || rw.collisions[0] != "a_new" {
		t.Errorf("collisions = %v", rw.collisions)
	}

	re, _ := newReplacer(`(\w+)@old`, "$1@new", true, false)
	if got := re.apply("u@old"); got != "u@new" {
		t.Errorf("regex = %s", got)
	}
	lit, _ := newReplacer("$1", "$2", false, false)
	if got := lit.apply("a$1"); got != "a$2" {
		t.Errorf("literal = %s", got)
	}
	if _, err := newReplacer("(", "x", true, false); err == nil {
		t.Error("bad regex should fail")
	}
}
