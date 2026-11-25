package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamludal/kvx/internal/output"
	"github.com/iamludal/kvx/internal/vault"
)

var (
	v2 = &vault.KV{Mount: "secret", Version: 2}
	v1 = &vault.KV{Mount: "kv1", Version: 1}
)

func TestIrreversible(t *testing.T) {
	cases := []struct {
		op   Op
		want bool
	}{
		{Op{Kind: Write, KV: v2}, false},
		{Op{Kind: Delete, KV: v2}, false},
		{Op{Kind: Undelete, KV: v2}, false},
		{Op{Kind: Delete, KV: v1}, true},
		{Op{Kind: Destroy, KV: v2}, true},
		{Op{Kind: Wipe, KV: v2}, true},
	}
	for _, c := range cases {
		if got := c.op.Irreversible(); got != c.want {
			t.Errorf("%s on KV v%d: Irreversible = %v", c.op.Kind, c.op.KV.Version, got)
		}
	}
}

func TestConfirm(t *testing.T) {
	out := &output.Printer{}
	soft := []Op{{Kind: Delete, KV: v2, Path: "a"}}
	hard := []Op{{Kind: Wipe, KV: v2, Path: "a"}}
	cases := []struct {
		name   string
		ops    []Op
		answer string
		ok     bool
	}{
		{"y accepts", soft, "y\n", true},
		{"YES accepts", soft, "YES\n", true},
		{"empty refuses", soft, "\n", false},
		{"n refuses", soft, "n\n", false},
		{"EOF refuses", soft, "", false},
		{"irreversible needs the word yes", hard, "y\n", false},
		{"irreversible accepts yes", hard, "yes\n", true},
	}
	for _, c := range cases {
		var errW bytes.Buffer
		err := confirm(out, c.ops, Options{In: strings.NewReader(c.answer), Err: &errW})
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if c.ops[0].Kind == Wipe && !strings.Contains(errW.String(), "cannot be undone") {
			t.Errorf("%s: missing irreversible warning, got %q", c.name, errW.String())
		}
	}
}

func TestRunDryRunAndEmpty(t *testing.T) {
	var stdout, stderr bytes.Buffer
	out := &output.Printer{W: &stdout}
	ops := []Op{
		{Kind: Wipe, KV: v2, Path: "z", Phase: 1, Label: "remove"},
		{Kind: Write, KV: v2, Path: "b", Label: "create", Detail: "from x"},
		{Kind: Write, KV: v2, Path: "a", Label: "create"},
	}
	err := Run(context.Background(), out, ops, []string{"secret/s (exists)"}, Options{DryRun: true, Err: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	got := stdout.String()
	// Sorted by phase, then path; summary counts actions and skips.
	ia, ib, iz := strings.Index(got, "secret/a"), strings.Index(got, "secret/b"), strings.Index(got, "secret/z")
	if !(ia < ib && ib < iz) {
		t.Errorf("plan order wrong:\n%s", got)
	}
	for _, want := range []string{"from x", "skip    secret/s (exists)", "Plan: 2 create, 1 remove, 1 skipped."} {
		if !strings.Contains(got, want) {
			t.Errorf("plan missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(stderr.String(), "Dry run") {
		t.Errorf("stderr = %q", stderr.String())
	}

	stderr.Reset()
	if err := Run(context.Background(), out, nil, nil, Options{Err: &stderr}); err != nil || !strings.Contains(stderr.String(), "Nothing to do") {
		t.Errorf("empty plan: err=%v stderr=%q", err, stderr.String())
	}

	// A refused confirmation changes nothing (the nil client is never used).
	stderr.Reset()
	err = Run(context.Background(), out, ops, nil, Options{In: strings.NewReader("no\n"), Err: &stderr})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Errorf("refused confirmation: err = %v", err)
	}
}

func TestPrintJSON(t *testing.T) {
	var buf bytes.Buffer
	out := &output.Printer{W: &buf, Format: output.JSON}
	if err := Print(out, []Op{{Kind: Delete, KV: v1, Path: "p", Label: "delete", Detail: "KV v1: permanent"}}, nil); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Changes []map[string]string `json:"changes"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Changes) != 1 || got.Changes[0]["path"] != "kv1/p" || got.Changes[0]["action"] != "delete" {
		t.Errorf("changes = %+v", got.Changes)
	}
}

func TestPhases(t *testing.T) {
	ops := []Op{{Phase: 0}, {Phase: 0}, {Phase: 1}, {Phase: 3}}
	got := phases(ops)
	if len(got) != 3 || len(got[0]) != 2 || len(got[1]) != 1 || len(got[2]) != 1 {
		t.Errorf("phases = %v", got)
	}
	if phases(nil) != nil {
		t.Error("no ops, no phases")
	}
}

func TestEqual(t *testing.T) {
	cases := []struct {
		a, b map[string]any
		want bool
	}{
		{map[string]any{"a": "1"}, map[string]any{"a": "1"}, true},
		{map[string]any{"a": "1"}, map[string]any{"a": json.Number("1")}, true},
		{map[string]any{"a": "1"}, map[string]any{"a": "2"}, false},
		{map[string]any{"a": "1"}, map[string]any{"b": "1"}, false},
		{map[string]any{"a": "1"}, map[string]any{"a": "1", "b": "2"}, false},
		{map[string]any{"n": map[string]any{"x": 1}}, map[string]any{"n": map[string]any{"x": 1}}, true},
		{nil, map[string]any{}, true},
	}
	for _, c := range cases {
		if got := Equal(c.a, c.b); got != c.want {
			t.Errorf("Equal(%v, %v) = %v", c.a, c.b, got)
		}
	}
}

func TestPaint(t *testing.T) {
	out := &output.Printer{Color: true}
	red := out.Red("x")
	if paint(out, Op{Kind: Delete, KV: v2}, "x") != red {
		t.Error("soft delete should be red")
	}
	if paint(out, Op{Kind: Write, KV: v2, Label: "create"}, "x") != out.Green("x") {
		t.Error("create should be green")
	}
	if paint(out, Op{Kind: Write, KV: v2, Label: "overwrite"}, "x") != out.Yellow("x") {
		t.Error("overwrite should be yellow")
	}
	if paint(out, Op{Kind: Wipe, KV: v2}, "x") != out.Red(out.Bold("x")) {
		t.Error("irreversible should be bold red")
	}
}
