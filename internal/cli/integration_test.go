package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/iamludal/kvx/internal/vault"
)

// Runs against a dev server: eval "$(scripts/dev-vault.sh)" && KVX_INTEGRATION=1 go test ./...
func integration(t *testing.T) (*vault.Client, string) {
	t.Helper()
	if os.Getenv("KVX_INTEGRATION") == "" {
		t.Skip("set KVX_INTEGRATION=1 with a dev Vault (scripts/dev-vault.sh)")
	}
	t.Setenv("KVX_BACKUP_DIR", t.TempDir())
	t.Setenv("NO_COLOR", "1")
	c, err := vault.NewClient(vault.Options{})
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("it-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = kvx(t, "rm", "-r", "--wipe", "--yes", "--no-backup", "secret/"+prefix) })
	return c, prefix
}

func kvx(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

func mustKvx(t *testing.T, args ...string) {
	t.Helper()
	if out, err := kvx(t, args...); err != nil {
		t.Fatalf("kvx %v: %v\n%s", args, err, out)
	}
}

func read(t *testing.T, c *vault.Client, full string) map[string]any {
	t.Helper()
	ref, err := c.Resolve(context.Background(), full)
	if err != nil {
		t.Fatal(err)
	}
	st, err := ref.KV.Stat(context.Background(), ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Data
}

func put(t *testing.T, c *vault.Client, full string, data map[string]any) {
	t.Helper()
	ref, err := c.Resolve(context.Background(), full)
	if err != nil {
		t.Fatal(err)
	}
	if err := ref.KV.Put(context.Background(), ref.Path, data, -1); err != nil {
		t.Fatal(err)
	}
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestIntegrationCopyMoveRemoveUndo(t *testing.T) {
	c, p := integration(t)
	put(t, c, "secret/"+p+"/src/a", map[string]any{"k": "v1"})
	put(t, c, "secret/"+p+"/src/a", map[string]any{"k": "v2"})
	put(t, c, "secret/"+p+"/src/sub/b", map[string]any{"x": "y"})

	// cp a tree, with history.
	mustKvx(t, "cp", "secret/"+p+"/src/", "secret/"+p+"/dst/", "--all-versions", "--yes")
	if got := jsonOf(read(t, c, "secret/"+p+"/dst/sub/b")); got != `{"x":"y"}` {
		t.Fatalf("dst/sub/b = %s", got)
	}

	// A differing destination is a conflict by default.
	put(t, c, "secret/"+p+"/dst/a", map[string]any{"k": "changed"})
	if _, err := kvx(t, "cp", "secret/"+p+"/src/", "secret/"+p+"/dst/", "--yes"); err == nil {
		t.Fatal("cp onto a differing destination should fail without --on-conflict")
	}
	mustKvx(t, "cp", "secret/"+p+"/src/", "secret/"+p+"/dst/", "--on-conflict", "overwrite", "--yes")
	if got := read(t, c, "secret/"+p+"/dst/a")["k"]; got != "v2" {
		t.Fatalf("overwrite: dst/a.k = %v", got)
	}

	// mv removes the source only after the copy.
	mustKvx(t, "mv", "secret/"+p+"/dst/sub/b", "secret/"+p+"/moved", "--yes")
	if read(t, c, "secret/"+p+"/dst/sub/b") != nil || read(t, c, "secret/"+p+"/moved") == nil {
		t.Fatal("mv did not move dst/sub/b")
	}

	// rm --wipe, then undo brings it back.
	mustKvx(t, "rm", "secret/"+p+"/moved", "--wipe", "--yes")
	if read(t, c, "secret/"+p+"/moved") != nil {
		t.Fatal("rm --wipe left the secret")
	}
	mustKvx(t, "undo", "--yes")
	if got := jsonOf(read(t, c, "secret/"+p+"/moved")); got != `{"x":"y"}` {
		t.Fatalf("undo did not restore the secret: %s", got)
	}

	// Dry run changes nothing.
	mustKvx(t, "rm", "-r", "secret/"+p+"/src", "--dry-run")
	if read(t, c, "secret/"+p+"/src/a") == nil {
		t.Fatal("--dry-run deleted something")
	}
}
