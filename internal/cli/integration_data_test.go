package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/iamludal/kvx/internal/dump"
	"github.com/iamludal/kvx/internal/vault"
)

func TestIntegrationExportImport(t *testing.T) {
	e := integration(t)
	e.seed()
	dir := t.TempDir()
	r := e.ref(e.s("app/db"))
	if err := r.KV.PutMetadata(context.Background(), r.Path, &vault.Meta{CustomMetadata: map[string]string{"owner": "team-a"}, MaxVersions: 7}); err != nil {
		t.Fatal(err)
	}

	// Tree export, JSON, with metadata.
	out := e.ok("export", e.s("app/"), "--with-metadata")
	d, err := dump.Decode([]byte(out), "")
	if err != nil {
		t.Fatalf("export is not readable: %v\n%s", err, out)
	}
	if d.Source != e.s("app/") || len(d.Secrets) != 3 || d.Secrets["db"].Data["password"] != "pw-two-long" {
		t.Fatalf("export = %+v", d)
	}
	if m := d.Secrets["db"].Metadata; m == nil || m.CustomMetadata["owner"] != "team-a" || m.MaxVersions != 7 {
		t.Errorf("db metadata = %+v", m)
	}
	if d.Secrets["api"].Metadata != nil {
		t.Error("default metadata should be left out")
	}
	file := filepath.Join(dir, "app.json")
	os.WriteFile(file, []byte(out), 0o600)

	// Import into a new tree, metadata included, then re-run (no-op).
	out = e.ok("import", e.s("restored/"), file, "--dry-run")
	if !strings.Contains(out, "Plan: 3 create.") || !strings.Contains(out, "with metadata") || e.stat(e.s("restored/db")).Exists {
		t.Fatalf("import --dry-run:\n%s", out)
	}
	e.ok("import", e.s("restored/"), file, "--yes")
	st := e.stat(e.s("restored/db"))
	if st.Data["password"] != "pw-two-long" || st.Meta.CustomMetadata["owner"] != "team-a" || st.Meta.MaxVersions != 7 {
		t.Errorf("restored/db = %v / %+v", st.Data, st.Meta)
	}
	if out := e.ok("import", e.s("restored/"), file, "--yes"); !strings.Contains(out, "3 skipped") {
		t.Errorf("import rerun:\n%s", out)
	}
	e.ok("undo", "--yes") // the first import's snapshot is the latest one that changed anything
	if e.stat(e.s("restored/db")).Exists {
		t.Error("undo should remove what import created")
	}

	// Conflicts, filters and stdin.
	e.ok("import", e.s("restored/"), file, "--yes")
	e.put(e.s("restored/api"), map[string]any{"API_KEY": "changed", "extra": "keep"})
	e.fails("already exist and differ", "import", e.s("restored/"), file, "--yes")
	out, _, err = e.runIn(strings.NewReader(readFile(t, file)), "import", e.s("restored/"), "-", "--on-conflict", "merge", "--include", "api", "--yes")
	if err != nil || !strings.Contains(out, "from stdin (+1 ~1 keys)") || strings.Contains(out, "restored/db") {
		t.Errorf("merge from stdin with --include: %v\n%s", err, out)
	}
	if got := jsonOf(e.data(e.s("restored/api"))); got != `{"API_KEY":"dup-value-123456","extra":"keep","timeout":"30"}` {
		t.Errorf("merge result = %s", got)
	}
	if out := e.ok("import", e.s("restored/"), file, "--exclude", "a*", "--on-conflict", "overwrite", "--yes"); !strings.Contains(out, "2 skipped") {
		t.Errorf("--exclude:\n%s", out)
	}
	e.fails("left no secret", "import", e.s("restored/"), file, "--include", "nope")

	// Single secret, YAML, with a version, into a directory (keeps its name)
	// and to an explicit path; KV v2 -> v1.
	yamlFile := filepath.Join(dir, "db.yaml")
	e.ok("export", e.s("app/db@1"), "--format", "yaml", "--file", yamlFile)
	if fi, err := os.Stat(yamlFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("--file mode = %v, %v", fi.Mode(), err)
	}
	e.ok("import", e.s("single/"), yamlFile, "--yes")
	e.ok("import", e.v1("copy"), yamlFile, "--yes")
	if e.data(e.s("single/db"))["password"] != "pw-one-long" || e.data(e.v1("copy"))["password"] != "pw-one-long" {
		t.Error("single secret import")
	}

	// dotenv both ways.
	envOut := e.ok("export", e.s("app/"), "--format", "dotenv")
	if !strings.Contains(envOut, "DB_PASSWORD=pw-two-long\n") || !strings.Contains(envOut, "API_API_KEY=") {
		t.Errorf("dotenv export:\n%s", envOut)
	}
	envFile := filepath.Join(dir, "app.env")
	os.WriteFile(envFile, []byte("# app\nexport USER=app\nPASSWORD='s3 cr3t'\n"), 0o600)
	e.ok("import", e.s("fromenv"), envFile, "--yes")
	if got := jsonOf(e.data(e.s("fromenv"))); got != `{"PASSWORD":"s3 cr3t","USER":"app"}` {
		t.Errorf("dotenv import = %s", got)
	}
	e.fails("give its full path", "import", e.s("dir/"), envFile, "--yes")

	// age, binary and armored.
	id, _ := age.GenerateX25519Identity()
	key := filepath.Join(dir, "key.txt")
	os.WriteFile(key, []byte(id.String()+"\n"), 0o600)
	enc := filepath.Join(dir, "backup.yaml.age")
	e.ok("export", e.s("app/"), "--format", "yaml", "--age-recipient", id.Recipient().String(), "--file", enc)
	if b, _ := os.ReadFile(enc); !dump.IsEncrypted(b) || strings.Contains(string(b), "pw-two-long") {
		t.Fatal("export --age-recipient is not encrypted")
	}
	e.fails("pass --age-identity", "import", e.s("aged/"), enc)
	e.ok("import", e.s("aged/"), enc, "--age-identity", key, "--yes")
	if e.data(e.s("aged/db"))["password"] != "pw-two-long" {
		t.Error("age import")
	}
	armored := e.ok("export", e.s("app/api"), "--age-recipient", id.Recipient().String(), "--armor")
	if !strings.HasPrefix(armored, "-----BEGIN AGE ENCRYPTED FILE-----") {
		t.Errorf("--armor:\n%s", armored)
	}
	out, _, err = e.runIn(strings.NewReader(armored), "import", e.s("aged-armor/"), "--age-identity", key, "--yes")
	if err != nil || e.data(e.s("aged-armor/api"))["timeout"] != "30" {
		t.Errorf("armored import from stdin: %v\n%s", err, out)
	}

	// Deleted secrets are left out; flag errors.
	e.softDelete(e.s("app/cache"))
	if _, stderr, err := e.run("", "export", e.s("app/")); err != nil || !strings.Contains(stderr, "Exported 2 secrets") || !strings.Contains(stderr, "1 skipped") {
		t.Errorf("export with a deleted secret: %v\n%s", err, stderr)
	}
	e.fails("is deleted", "export", e.s("app/cache"))
	e.fails("only applies to a single secret", "export", e.s("app/@1"))
	e.fails("--format must be", "export", e.s("app/"), "--format", "xml")
	e.fails("cannot hold metadata", "export", e.s("app/"), "--format", "dotenv", "--with-metadata")
	e.fails("--armor only applies", "export", e.s("app/"), "--armor")
	e.fails("neither an age/SSH recipient", "export", e.s("app/"), "--age-recipient", "nope")
	e.fails("--format must be", "import", e.s("x/"), file, "--format", "xml")
	e.fails("--on-conflict must be", "import", e.s("x/"), file, "--on-conflict", "maybe")
	e.fails("cannot import into a version", "import", e.s("x@2"), yamlFile)
	e.fails("no such file", "import", e.s("x/"), filepath.Join(dir, "missing.json"))
	e.fails("parsing JSON", "import", e.s("x/"), envFile, "--format", "json")
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
