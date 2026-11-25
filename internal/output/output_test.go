package output

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseFormat(t *testing.T) {
	for _, s := range []string{"table", "JSON", "yaml"} {
		if _, err := ParseFormat(s); err != nil {
			t.Errorf("ParseFormat(%q): %v", s, err)
		}
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Error("ParseFormat(xml) should fail")
	}
}

func TestMaskAndFingerprint(t *testing.T) {
	if Mask("") != "<empty>" || Mask(nil) != "<empty>" {
		t.Error("empty values should be shown as <empty>")
	}
	m := Mask("hunter2")
	if !strings.HasPrefix(m, "***") || len(m) != 9 || strings.Contains(m, "hunter2") {
		t.Errorf("Mask = %q", m)
	}
	if Mask("hunter2") != m || Mask("hunter3") == m {
		t.Error("masks must be stable for equal values and differ for different ones")
	}
	if len(Fingerprint("x")) != 64 {
		t.Error("Fingerprint should be a hex sha256")
	}
}

func TestStringify(t *testing.T) {
	cases := map[string]any{
		"":          nil,
		"s":         "s",
		"42":        42,
		"true":      true,
		`{"a":1}`:   map[string]any{"a": 1},
		`["x","y"]`: []any{"x", "y"},
	}
	for want, in := range cases {
		if got := Stringify(in); got != want {
			t.Errorf("Stringify(%#v) = %q, want %q", in, got, want)
		}
	}
}

func TestPrinterValues(t *testing.T) {
	d := map[string]any{"k": "secret"}
	masked := (&Printer{}).Values(d)
	if masked["k"] == "secret" {
		t.Error("values must be masked by default")
	}
	if (&Printer{}).Value("secret") == "secret" {
		t.Error("Value must mask by default")
	}
	shown := &Printer{ShowSecrets: true}
	if shown.Values(d)["k"] != "secret" || shown.Value("secret") != "secret" {
		t.Error("ShowSecrets must reveal values")
	}
}

func TestStructuredAndTable(t *testing.T) {
	var buf bytes.Buffer
	p := &Printer{W: &buf, Format: JSON}
	if ok, err := p.Structured(map[string]int{"a": 1}); !ok || err != nil || !strings.Contains(buf.String(), `"a": 1`) {
		t.Errorf("JSON: ok=%v err=%v out=%q", ok, err, buf.String())
	}
	buf.Reset()
	p.Format = YAML
	if ok, _ := p.Structured(map[string]int{"a": 1}); !ok || strings.TrimSpace(buf.String()) != "a: 1" {
		t.Errorf("YAML out=%q", buf.String())
	}
	buf.Reset()
	p.Format = Table
	if ok, _ := p.Structured(1); ok {
		t.Error("table format must let the caller render")
	}
	_ = p.Table([]string{"A", "B"}, [][]string{{"x", "yy"}})
	if got := buf.String(); got != "A  B\nx  yy\n" {
		t.Errorf("Table = %q", got)
	}
}

func TestColorsAndPlural(t *testing.T) {
	off := &Printer{}
	if off.Red("x") != "x" || off.Bold("x") != "x" || off.Blue("x") != "x" {
		t.Error("no color codes without Color")
	}
	on := &Printer{Color: true}
	for _, s := range []string{on.Red("x"), on.Green("x"), on.Yellow("x"), on.Blue("x"), on.Dim("x"), on.Bold("x")} {
		if !strings.HasPrefix(s, "\x1b[") || !strings.HasSuffix(s, "\x1b[0m") {
			t.Errorf("colored = %q", s)
		}
	}
	if Plural(1, "secret") != "1 secret" || Plural(0, "secret") != "0 secrets" {
		t.Error("Plural")
	}
	if New(&bytes.Buffer{}, Table, false, false).Color {
		t.Error("no color when not writing to a terminal")
	}
}

func TestSortedKeys(t *testing.T) {
	got := SortedKeys(map[string]int{"b": 1, "a": 2})
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("SortedKeys = %v", got)
	}
}
