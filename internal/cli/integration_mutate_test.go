package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamludal/kvx/internal/vault"
)

func TestIntegrationCp(t *testing.T) {
	e := integration(t)
	e.seed()

	// Plan only.
	out := e.ok("cp", e.s("app/"), e.s("dst/"), "--dry-run")
	if !strings.Contains(out, "Plan: 3 create.") || e.stat(e.s("dst/db")).Exists {
		t.Fatalf("cp --dry-run:\n%s", out)
	}
	var plan struct {
		Changes []struct{ Action, Path, Detail string }
	}
	e.json(&plan, "cp", e.s("app/"), e.s("dst/"), "--dry-run")
	if len(plan.Changes) != 3 || plan.Changes[0].Action != "create" || plan.Changes[0].Path != e.s("dst/api") {
		t.Errorf("cp plan json = %+v", plan)
	}

	// Tree copy with history and metadata.
	r := e.ref(e.s("app/db"))
	if err := r.KV.PutMetadata(context.Background(), r.Path, &vault.Meta{CustomMetadata: map[string]string{"owner": "team-a"}, MaxVersions: 7}); err != nil {
		t.Fatal(err)
	}
	e.ok("cp", e.s("app/"), e.s("dst/"), "--all-versions", "--with-metadata", "--verify", "--yes")
	st := e.stat(e.s("dst/db"))
	if st.Version != 2 || st.Data["password"] != "pw-two-long" || st.Meta.CustomMetadata["owner"] != "team-a" || st.Meta.MaxVersions != 7 {
		t.Errorf("dst/db after cp = %+v / %+v", st.Data, st.Meta)
	}
	if got := e.ok("get", e.s("dst/db@1"), "-f", "password"); got != "pw-one-long" {
		t.Errorf("history not copied: dst/db@1 = %q", got)
	}

	// History skips deleted versions.
	e.put(e.s("hist"), map[string]any{"v": "1"})
	e.put(e.s("hist"), map[string]any{"v": "2"})
	e.put(e.s("hist"), map[string]any{"v": "3"})
	e.ok("rm", e.s("hist"), "--versions", "2", "--yes")
	e.ok("cp", e.s("hist"), e.s("hist-copy"), "--all-versions", "--yes")
	if st := e.stat(e.s("hist-copy")); st.Version != 2 || st.Data["v"] != "3" {
		t.Errorf("history copy = v%d %v", st.Version, st.Data)
	}

	// Re-running is a no-op.
	if out := e.ok("cp", e.s("app/"), e.s("dst/"), "--yes"); !strings.Contains(out, "3 skipped") {
		t.Errorf("cp rerun:\n%s", out)
	}

	// Conflicts.
	e.put(e.s("dst/api"), map[string]any{"API_KEY": "changed", "extra": "keep"})
	e.fails("already exist and differ", "cp", e.s("app/"), e.s("dst/"), "--yes")
	if out := e.ok("cp", e.s("app/"), e.s("dst/"), "--on-conflict", "skip", "--yes"); !strings.Contains(out, e.s("dst/api")+" (exists)") {
		t.Errorf("--on-conflict skip:\n%s", out)
	}
	if e.data(e.s("dst/api"))["API_KEY"] != "changed" {
		t.Error("skip must not write")
	}
	out = e.ok("cp", e.s("app/api"), e.s("dst/api"), "--on-conflict", "merge", "--yes")
	if !strings.Contains(out, "(+1 ~1 keys)") {
		t.Errorf("merge plan:\n%s", out)
	}
	if got := jsonOf(e.data(e.s("dst/api"))); got != `{"API_KEY":"dup-value-123456","extra":"keep","timeout":"30"}` {
		t.Errorf("merge result = %s", got)
	}
	if out := e.ok("cp", e.s("app/api"), e.s("dst/api"), "--on-conflict", "merge", "--yes"); !strings.Contains(out, "nothing to merge") {
		t.Errorf("merge again:\n%s", out)
	}
	e.ok("cp", e.s("app/api"), e.s("dst/api"), "--on-conflict", "overwrite", "--yes")
	if got := jsonOf(e.data(e.s("dst/api"))); got != `{"API_KEY":"dup-value-123456","timeout":"30"}` {
		t.Errorf("overwrite result = %s", got)
	}

	// Single secret into a directory, v2 -> v1, deleted sources, remote flag.
	e.ok("cp", e.s("app/db"), e.s("archive/"), "--yes")
	if e.data(e.s("archive/db")) == nil {
		t.Error("cp secret dir/ should land in dir/<name>")
	}
	e.ok("cp", e.s("app/"), e.v1("app/"), "--all-versions", "--yes")
	if e.data(e.v1("app/db"))["password"] != "pw-two-long" {
		t.Error("cp v2 -> v1 failed")
	}
	if out := e.ok("cp", e.s(""), e.s("all/"), "--include", "gone", "--yes"); !strings.Contains(out, "latest version deleted") {
		t.Errorf("deleted source should be skipped:\n%s", out)
	}
	e.ok("cp", e.s("other/copy"), e.s("remote-copy"), "--to-address", os.Getenv("VAULT_ADDR"), "--yes")
	if e.data(e.s("remote-copy")) == nil {
		t.Error("cp --to-address failed")
	}

	e.fails("are the same", "cp", e.s("app/db"), e.s("app/db"), "--yes")
	e.fails("--on-conflict must be", "cp", e.s("app/"), e.s("x/"), "--on-conflict", "yolo")
	e.fails("nothing found", "cp", e.s("nope/"), e.s("x/"))
	e.fails("destination vault", "cp", e.s("app/"), e.s("x/"), "--to-token", "t", "--to-address", "::bad")
}

func TestIntegrationMv(t *testing.T) {
	e := integration(t)
	e.seed()

	e.ok("mv", e.s("app/db"), e.s("renamed/db"), "--yes")
	st := e.stat(e.s("renamed/db"))
	if e.stat(e.s("app/db")).Exists || st.Version != 2 || st.Data["password"] != "pw-two-long" {
		t.Fatalf("mv secret: src exists=%v dst=%+v", e.stat(e.s("app/db")).Exists, st)
	}

	// A directory, latest version only.
	out := e.ok("mv", e.s("app/"), e.s("moved/"), "--history=false", "--yes")
	if !strings.Contains(out, "2 create, 2 remove") {
		t.Errorf("mv dir plan:\n%s", out)
	}
	if e.stat(e.s("app/api")).Exists || e.data(e.s("moved/api")) == nil {
		t.Error("mv dir failed")
	}

	// A conflict stops everything: the source stays.
	e.put(e.s("conflict/copy"), map[string]any{"key": "different"})
	e.fails("already exist", "mv", e.s("other/"), e.s("conflict/"), "--yes")
	if e.data(e.s("other/copy")) == nil {
		t.Error("failed mv must keep the source")
	}
	// Identical destination: nothing to write, the source is removed.
	e.put(e.s("same/copy"), map[string]any{"key": "dup-value-123456"})
	e.ok("mv", e.s("other/"), e.s("same/"), "--yes")
	if e.stat(e.s("other/copy")).Exists {
		t.Error("mv onto an identical destination should remove the source")
	}
}

func TestIntegrationRm(t *testing.T) {
	e := integration(t)
	e.seed()

	e.fails("is a directory", "rm", e.s("app"))
	e.fails("exclusive", "rm", e.s("app/db"), "--destroy", "--wipe")
	e.fails("does not apply", "rm", e.s("app/db"), "--wipe", "--versions", "1")
	e.fails("invalid version", "rm", e.s("app/db"), "--versions", "x")
	e.fails("needs KV v2", "rm", e.v1("team/a"), "--versions", "1")
	e.fails("--older-than", "rm", e.s("app/db"), "--older-than", "soon")

	// Soft delete, then nothing left to soft-delete.
	e.ok("rm", e.s("app/cache"), "--yes")
	if st := e.stat(e.s("app/cache")); !st.Exists || st.Data != nil {
		t.Errorf("soft delete: %+v", st)
	}
	if out := e.ok("rm", e.s("app/cache"), "--yes"); !strings.Contains(out, "already deleted") {
		t.Errorf("rm twice:\n%s", out)
	}

	// Destroy one version, keep the rest.
	e.ok("rm", e.s("app/db"), "--destroy", "--versions", "1", "--yes")
	if st := e.stat(e.s("app/db")); !st.Meta.Destroyed(1) || st.Meta.Destroyed(2) || st.Data == nil {
		t.Errorf("destroy v1: %+v", st.Meta)
	}
	e.ok("rm", e.s("app/db"), "--destroy", "--yes") // latest
	if st := e.stat(e.s("app/db")); !st.Meta.Destroyed(2) || st.Data != nil {
		t.Errorf("destroy latest: %+v", st.Meta)
	}
	// Soft-delete chosen versions.
	e.put(e.s("multi"), map[string]any{"v": "1"})
	e.put(e.s("multi"), map[string]any{"v": "2"})
	e.ok("rm", e.s("multi"), "--versions", "1", "--yes")
	if _, _, err := e.run("", "get", e.s("multi@1")); err == nil {
		t.Error("multi@1 should be deleted")
	}
	if e.data(e.s("multi"))["v"] != "2" {
		t.Error("multi latest should be untouched")
	}

	// Filters.
	e.put(e.s("empty/a"), map[string]any{"k": " "})
	if out := e.ok("rm", "-r", e.s(""), "--only-empty", "--dry-run"); !strings.Contains(out, "Plan: 1 delete.") || strings.Contains(out, "skip") {
		t.Errorf("--only-empty:\n%s", out)
	}
	if out, errOut, _ := e.run("", "rm", "-r", e.s(""), "--older-than", "30d", "--yes"); !strings.Contains(errOut, "Nothing to do") {
		t.Errorf("--older-than 30d:\n%s%s", out, errOut)
	}
	if out := e.ok("rm", "-r", e.v1(""), "--older-than", "1d", "--dry-run"); !strings.Contains(out, "needs KV v2") {
		t.Errorf("--older-than on kv1:\n%s", out)
	}

	// Wipe a tree; KV v1 delete is final.
	out := e.ok("rm", "-r", e.s("other"), "--wipe", "--yes")
	if !strings.Contains(out, "metadata and 1 version") || e.stat(e.s("other/copy")).Exists {
		t.Errorf("wipe:\n%s", out)
	}
	if out := e.ok("rm", e.v1("team/a"), "--dry-run"); !strings.Contains(out, "KV v1: permanent") {
		t.Errorf("kv1 rm plan:\n%s", out)
	}
	e.ok("rm", e.v1("team/a"), "--yes")
	if e.stat(e.v1("team/a")).Exists {
		t.Error("kv1 rm failed")
	}
	// Several paths at once.
	e.ok("rm", e.s("app/api"), e.s("multi"), "--yes")
	if e.data(e.s("app/api")) != nil || e.data(e.s("multi")) != nil {
		t.Error("rm with several paths")
	}
}

func TestIntegrationUndeleteRollback(t *testing.T) {
	e := integration(t)
	e.seed()

	e.fails("use -r", "undelete", e.s("app"))
	e.fails("needs KV v2", "undelete", e.v1("team/a"))
	e.ok("undelete", e.s("gone"), "--yes")
	if e.data(e.s("gone"))["t"] != "x" {
		t.Error("undelete gone")
	}
	if _, errOut, _ := e.run("", "undelete", e.s("gone"), "--yes"); !strings.Contains(errOut, "Nothing to do") {
		t.Errorf("undelete a live secret: %s", errOut)
	}

	e.ok("rm", "-r", e.s("app"), "--yes")
	e.ok("undelete", "-r", e.s("app"), "--yes")
	if e.data(e.s("app/db")) == nil || e.data(e.s("app/api")) == nil {
		t.Error("undelete -r")
	}

	e.ok("rm", e.s("app/db"), "--versions", "1", "--yes")
	e.ok("undelete", e.s("app/db@1"), "--yes")
	if got := e.ok("get", e.s("app/db@1"), "-f", "password"); got != "pw-one-long" {
		t.Errorf("undelete @1 = %q", got)
	}
	e.ok("rm", e.s("app/db"), "--destroy", "--versions", "1", "--yes")
	if out := e.ok("undelete", e.s("app/db@1"), "--yes"); !strings.Contains(out, "destroyed, cannot be restored") {
		t.Errorf("undelete destroyed:\n%s", out)
	}

	// rollback
	e.fails("give the version", "rollback", e.s("app/db"))
	e.fails("deleted or destroyed", "rollback", e.s("app/db@1"))
	e.put(e.s("rb"), map[string]any{"v": "one"})
	e.put(e.s("rb"), map[string]any{"v": "two"})
	out := e.ok("rollback", e.s("rb@1"), "--yes")
	if !strings.Contains(out, "v2 -> new v3 with the data of v1 (~1 keys)") {
		t.Errorf("rollback plan:\n%s", out)
	}
	if st := e.stat(e.s("rb")); st.Version != 3 || st.Data["v"] != "one" {
		t.Errorf("rollback result = %+v", st)
	}
	if out := e.ok("rollback", e.s("rb@1"), "--yes"); !strings.Contains(out, "already equals v1") {
		t.Errorf("rollback no-op:\n%s", out)
	}
}

func TestIntegrationUndo(t *testing.T) {
	e := integration(t)
	e.seed()

	e.fails("no snapshots yet", "undo")

	// Wipe, undo, redo.
	e.ok("rm", e.s("app/db"), "--wipe", "--yes")
	e.ok("undo", "--yes")
	if e.data(e.s("app/db"))["password"] != "pw-two-long" {
		t.Fatal("undo did not restore the wiped secret")
	}
	e.ok("undo", "--yes") // undo the undo
	if e.stat(e.s("app/db")).Exists {
		t.Fatal("second undo should redo the wipe")
	}

	// Undo a cp: created secrets go away, overwritten ones come back,
	// soft-deleted destinations are deleted again.
	e.put(e.s("dst/api"), map[string]any{"API_KEY": "before"})
	e.put(e.s("dst/cache"), map[string]any{"url": "before"})
	e.softDelete(e.s("dst/cache"))
	e.ok("cp", e.s("app/"), e.s("dst/"), "--on-conflict", "overwrite", "--yes")
	out, errOut, err := e.run("", "undo", "--yes")
	if err != nil {
		t.Fatalf("undo cp: %v\n%s", err, errOut)
	}
	for _, want := range []string{"restore", "delete", "was soft-deleted before"} {
		if !strings.Contains(out, want) {
			t.Errorf("undo cp plan missing %q:\n%s", want, out)
		}
	}
	if e.data(e.s("dst/api"))["API_KEY"] != "before" || e.data(e.s("dst/cache")) != nil {
		t.Errorf("undo cp: api=%v cache=%v", e.data(e.s("dst/api")), e.data(e.s("dst/cache")))
	}

	// List, pick by id prefix, nothing left to do.
	var list []struct {
		ID      string
		Secrets int
		Command string
	}
	e.json(&list, "undo", "--list")
	if len(list) != 5 {
		t.Fatalf("undo --list = %+v", list)
	}
	if out := e.ok("undo", "--list"); !strings.Contains(out, "SECRETS") {
		t.Errorf("undo --list table:\n%s", out)
	}
	oldest := list[len(list)-1].ID // the rm --wipe
	if out, errOut, _ := e.run("", "undo", oldest[:len(oldest)-3], "--dry-run"); !strings.Contains(errOut, oldest) || !strings.Contains(out, "create") {
		t.Errorf("undo by prefix:\n%s%s", out, errOut)
	}
	e.ok("undo", list[0].ID, "--yes") // the last undo: redo it
	if _, errOut, _ := e.run("", "undo", list[0].ID, "--yes"); !strings.Contains(errOut, "Nothing to do") {
		t.Errorf("undo already applied: %s", errOut)
	}
	e.fails("no snapshot", "undo", "19990101")
	e.fails("ambiguous", "undo", "2")

	// --no-backup leaves nothing to undo.
	before := len(list)
	e.ok("rm", e.s("other/copy"), "--yes", "--no-backup")
	e.json(&list, "undo", "--list")
	if len(list) != before+1 { // + the redo above (the no-op undo saves nothing), not the rm
		t.Errorf("--no-backup still saved a snapshot: %d snapshots", len(list))
	}

	// Corrupted snapshot store.
	dir := os.Getenv("KVX_BACKUP_DIR")
	if err := os.WriteFile(filepath.Join(dir, "zz.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.fails("reading", "undo", "--list")
}

func TestIntegrationConfirm(t *testing.T) {
	e := integration(t)
	e.seed()

	// No terminal and no --yes: refused.
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, _, err := e.runIn(f, "rm", e.s("app/db")); err == nil || !strings.Contains(err.Error(), "without a terminal") {
		t.Errorf("rm without tty: %v", err)
	}

	_, errOut, err := e.run("n\n", "rm", e.s("app/db"))
	if err == nil || !strings.Contains(errOut, "Apply 1 change? [y/N]") || e.data(e.s("app/db")) == nil {
		t.Errorf("answer n: err=%v stderr=%q", err, errOut)
	}
	if _, _, err := e.run("y\n", "rm", e.s("app/db")); err != nil || e.data(e.s("app/db")) != nil {
		t.Errorf("answer y: %v", err)
	}

	// Irreversible: y is not enough, yes is.
	if _, errOut, err := e.run("y\n", "rm", e.s("app/api"), "--wipe"); err == nil || !strings.Contains(errOut, "Type yes to apply") || !e.stat(e.s("app/api")).Exists {
		t.Errorf("wipe with y: err=%v stderr=%q", err, errOut)
	}
	if _, _, err := e.run("yes\n", "rm", e.s("app/api"), "--wipe"); err != nil || e.stat(e.s("app/api")).Exists {
		t.Errorf("wipe with yes: %v", err)
	}
}

// hookReader runs fn the first time the confirmation prompt reads stdin,
// i.e. between the plan and the apply.
type hookReader struct {
	fn   func()
	r    io.Reader
	done bool
}

func (h *hookReader) Read(p []byte) (int, error) {
	if !h.done {
		h.done = true
		h.fn()
	}
	return h.r.Read(p)
}

func TestIntegrationCheckAndSet(t *testing.T) {
	e := integration(t)
	e.seed()
	e.put(e.s("dst/db"), map[string]any{"v": "planned"})

	// Someone writes dst/db after kvx made its plan: the write must fail.
	in := &hookReader{r: strings.NewReader("y\n"), fn: func() {
		e.put(e.s("dst/db"), map[string]any{"v": "concurrent"})
	}}
	_, errOut, err := e.runIn(in, "cp", e.s("app/db"), e.s("dst/db"), "--on-conflict", "overwrite")
	if err == nil || !strings.Contains(err.Error(), "changed since the plan was made") {
		t.Fatalf("expected a check-and-set failure, got %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "Stopped after 0 of 1 changes") {
		t.Errorf("stderr = %q", errOut)
	}
	if e.data(e.s("dst/db"))["v"] != "concurrent" {
		t.Error("the concurrent write must win")
	}

	// Same for a create: the destination appears after the plan.
	in = &hookReader{r: strings.NewReader("y\n"), fn: func() {
		e.put(e.s("dst/new"), map[string]any{"v": "concurrent"})
	}}
	if _, _, err := e.runIn(in, "cp", e.s("app/db"), e.s("dst/new")); err == nil || !strings.Contains(err.Error(), "check-and-set 0 failed") {
		t.Errorf("create race: %v", err)
	}
}

func TestIntegrationStubs(t *testing.T) {
	e := integration(t)
	for _, args := range [][]string{
		{"replace", "secret/", "a", "b"}, {"normalize", "secret/"}, {"edit", "secret/x"},
		{"exec", "secret/x", "true"},
	} {
		e.fails("not implemented yet", args...)
	}
}
