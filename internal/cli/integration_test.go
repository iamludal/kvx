package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamludal/kvx/internal/vault"
)

// These tests run every command against a real dev server:
//
//	eval "$(scripts/dev-vault.sh)" && KVX_INTEGRATION=1 go test ./...
//
// Each test works under its own random prefix (secret/<p>/, kv1/<p>/), so
// tests are independent and can run against a server that has other data.

type env struct {
	t *testing.T
	c *vault.Client
	p string // unique prefix
}

func integration(t *testing.T) *env {
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
	e := &env{t: t, c: c, p: fmt.Sprintf("it-%d", time.Now().UnixNano())}
	t.Cleanup(func() {
		for _, m := range []string{"secret", "kv1"} {
			_, _, _ = e.run("", "rm", "-r", "--wipe", "--yes", "--no-backup", m+"/"+e.p)
		}
	})
	return e
}

// s returns a full path under the test prefix of the secret/ mount.
func (e *env) s(rel string) string { return "secret/" + e.p + "/" + rel }

// v1 returns a full path under the test prefix of the kv1/ mount.
func (e *env) v1(rel string) string { return "kv1/" + e.p + "/" + rel }

// run executes kvx with args. stdin feeds confirmation prompts.
func (e *env) run(stdin string, args ...string) (string, string, error) {
	return e.runIn(strings.NewReader(stdin), args...)
}

func (e *env) runIn(stdin io.Reader, args ...string) (string, string, error) {
	root := NewRoot()
	var out, errW bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errW)
	root.SetIn(stdin)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), errW.String(), err
}

// ok runs kvx and fails the test on error. It returns stdout.
func (e *env) ok(args ...string) string {
	e.t.Helper()
	out, errOut, err := e.run("", args...)
	if err != nil {
		e.t.Fatalf("kvx %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, out, errOut)
	}
	return out
}

// fails runs kvx, expects an error containing want, and returns it.
func (e *env) fails(want string, args ...string) error {
	e.t.Helper()
	out, _, err := e.run("", args...)
	if err == nil {
		e.t.Fatalf("kvx %s: expected an error containing %q, got success:\n%s", strings.Join(args, " "), want, out)
	}
	if !strings.Contains(err.Error(), want) {
		e.t.Fatalf("kvx %s: error %q does not contain %q", strings.Join(args, " "), err, want)
	}
	return err
}

// json runs kvx with -o json and decodes stdout into v.
func (e *env) json(v any, args ...string) {
	e.t.Helper()
	out := e.ok(append(args, "-o", "json")...)
	rv := reflect.ValueOf(v).Elem() // json.Unmarshal reuses slice elements: start clean
	rv.Set(reflect.Zero(rv.Type()))
	if err := json.Unmarshal([]byte(out), v); err != nil {
		e.t.Fatalf("kvx %s: invalid JSON: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func (e *env) ref(full string) vault.Ref {
	e.t.Helper()
	r, err := e.c.Resolve(context.Background(), full)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) put(full string, data map[string]any) {
	e.t.Helper()
	r := e.ref(full)
	if err := r.KV.Put(context.Background(), r.Path, data, -1); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) stat(full string) vault.State {
	e.t.Helper()
	r := e.ref(full)
	st, err := r.KV.Stat(context.Background(), r.Path)
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *env) data(full string) map[string]any { e.t.Helper(); return e.stat(full).Data }

func (e *env) softDelete(full string) {
	e.t.Helper()
	r := e.ref(full)
	if err := r.KV.SoftDelete(context.Background(), r.Path, nil); err != nil {
		e.t.Fatal(err)
	}
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }

func exitCode(err error) int {
	var ee ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	if err != nil {
		return 2
	}
	return 0
}

// seed creates a small tree:
//
//	secret/<p>/app/db      user, password (2 versions), host
//	secret/<p>/app/api     API_KEY (same value as other/copy.key), timeout
//	secret/<p>/app/cache   url, empty=""
//	secret/<p>/other/copy  key
//	secret/<p>/gone        latest version soft-deleted
//	kv1/<p>/team/a         apiKey
func (e *env) seed() {
	e.t.Helper()
	e.put(e.s("app/db"), map[string]any{"user": "app", "password": "pw-one-long", "host": "db-old.internal"})
	e.put(e.s("app/db"), map[string]any{"user": "app", "password": "pw-two-long", "host": "db-old.internal"})
	e.put(e.s("app/api"), map[string]any{"API_KEY": "dup-value-123456", "timeout": "30"})
	e.put(e.s("app/cache"), map[string]any{"url": "redis://cache:6379", "empty": ""})
	e.put(e.s("other/copy"), map[string]any{"key": "dup-value-123456"})
	e.put(e.s("gone"), map[string]any{"t": "x"})
	e.softDelete(e.s("gone"))
	e.put(e.v1("team/a"), map[string]any{"apiKey": "v"})
}

// ---------------------------------------------------------------- read

func TestIntegrationLs(t *testing.T) {
	e := integration(t)
	e.seed()

	type entry struct {
		Path string `json:"path"`
		Dir  bool   `json:"dir"`
	}
	var top []entry
	e.json(&top, "ls", e.s(""))
	if jsonOf(top) != jsonOf([]entry{{e.s("app/"), true}, {e.s("gone"), false}, {e.s("other/"), true}}) {
		t.Errorf("ls = %s", jsonOf(top))
	}

	var long []struct {
		Path    string
		Version int
	}
	e.json(&long, "ls", "-l", e.s("app"))
	if len(long) != 3 || long[2].Path != e.s("app/db") || long[2].Version != 2 {
		t.Errorf("ls -l = %+v", long)
	}
	if out := e.ok("ls", "-l", e.s("app")); !strings.Contains(out, "v2") || !strings.Contains(out, "VERSION") {
		t.Errorf("ls -l table:\n%s", out)
	}

	out := e.ok("ls", "-r", e.s(""))
	if got := strings.Count(out, "\n"); got != 5 {
		t.Errorf("ls -r: want 5 secrets, got %d:\n%s", got, out)
	}
	if out := e.ok("ls", "-r", e.s(""), "--include", "**/db", "--include", "gone"); strings.Count(out, "\n") != 2 {
		t.Errorf("ls -r --include:\n%s", out)
	}
	if out := e.ok("ls", "-r", e.s(""), "--exclude", "app/**"); strings.Contains(out, "/app/") {
		t.Errorf("ls -r --exclude:\n%s", out)
	}
	if out := e.ok("ls", "-r", e.s(""), "-d", "1"); strings.TrimSpace(out) != e.s("gone") {
		t.Errorf("ls -r -d 1:\n%s", out)
	}
	if out := e.ok("ls", e.v1("")); !strings.Contains(out, e.v1("team/")) {
		t.Errorf("ls kv1:\n%s", out)
	}
	e.fails("glob", "ls", "-r", e.s(""), "--include", "[")
}

func TestIntegrationTree(t *testing.T) {
	e := integration(t)
	e.seed()

	out := e.ok("tree", e.s(""), "-k")
	for _, want := range []string{"├── app/", "│   └── db", "├─ password", "gone (deleted)", "2 directories, 5 secrets"} {
		if !strings.Contains(out, want) {
			t.Errorf("tree -k missing %q:\n%s", want, out)
		}
	}

	out = e.ok("tree", e.s("app"), "--values")
	if strings.Contains(out, "pw-two-long") || !strings.Contains(out, "password = ***") || !strings.Contains(out, "empty = <empty>") {
		t.Errorf("tree --values must mask:\n%s", out)
	}
	if out := e.ok("tree", e.s("app"), "--values", "-S"); !strings.Contains(out, "password = pw-two-long") {
		t.Errorf("tree --values -S:\n%s", out)
	}

	out = e.ok("tree", e.s(""), "-d", "1", "--dirs-only")
	if !strings.Contains(out, "app/") || !strings.Contains(out, "other/") || strings.Contains(out, "gone") {
		t.Errorf("tree --dirs-only -d 1:\n%s", out)
	}
	if !strings.Contains(e.ok("tree", e.s(""), "-d", "1"), "1 secret") {
		t.Error("tree -d 1 should still count the top-level secret")
	}

	var keys map[string]map[string][]string
	e.json(&keys, "tree", e.s(""), "-k", "--exclude", "gone")
	if jsonOf(keys["app"]["db"]) != `["host","password","user"]` {
		t.Errorf("tree -k json = %s", jsonOf(keys))
	}
	var vals map[string]map[string]string
	e.json(&vals, "tree", e.s("other"), "--values", "-S")
	if vals["copy"]["key"] != "dup-value-123456" {
		t.Errorf("tree --values json = %s", jsonOf(vals))
	}
	var plain map[string]any
	e.json(&plain, "tree", e.s(""), "-o", "yaml") // last -o wins: json
	if _, ok := plain["gone"]; !ok {
		t.Errorf("tree json = %s", jsonOf(plain))
	}
	if out := e.ok("tree", e.s(""), "-o", "yaml"); !strings.Contains(out, "app:") {
		t.Errorf("tree yaml:\n%s", out)
	}

	// A path that is both a secret and a directory.
	e.put(e.s("app"), map[string]any{"k": "v"})
	if out := e.ok("tree", e.s("")); !strings.Contains(out, "app/ (also a secret)") {
		t.Errorf("tree secret+dir:\n%s", out)
	}
}

func TestIntegrationGet(t *testing.T) {
	e := integration(t)
	e.seed()

	out := e.ok("get", e.s("app/db"))
	if !strings.Contains(out, "pw-two-long") || !strings.Contains(out, "KEY") {
		t.Errorf("get:\n%s", out)
	}
	if got := e.ok("get", e.s("app/db"), "-f", "password"); got != "pw-two-long" {
		t.Errorf("get -f = %q", got)
	}
	if got := e.ok("get", e.s("app/db@1"), "-f", "password"); got != "pw-one-long" {
		t.Errorf("get @1 -f = %q", got)
	}
	if got := e.ok("get", e.s("app/api"), "--env"); got != "export API_KEY='dup-value-123456'\nexport TIMEOUT='30'\n" {
		t.Errorf("get --env = %q", got)
	}
	var sub map[string]string
	e.json(&sub, "get", e.s("app/db"), "user", "host")
	if jsonOf(sub) != `{"host":"db-old.internal","user":"app"}` {
		t.Errorf("get keys = %s", jsonOf(sub))
	}
	if out := e.ok("get", e.s("app/db"), "--mask"); strings.Contains(out, "pw-two-long") {
		t.Errorf("get --mask:\n%s", out)
	}
	var masked map[string]string
	e.json(&masked, "get", e.s("app/db"), "--mask")
	if strings.Contains(jsonOf(masked), "pw-two-long") {
		t.Errorf("get --mask json = %s", jsonOf(masked))
	}

	var withMeta struct {
		Data     map[string]string
		Metadata struct{ CurrentVersion, Versions int }
	}
	e.json(&withMeta, "get", e.s("app/db"), "--meta")
	if withMeta.Metadata.CurrentVersion != 2 || withMeta.Metadata.Versions != 2 || withMeta.Data["user"] != "app" {
		t.Errorf("get --meta = %+v", withMeta)
	}
	if out := e.ok("get", e.s("app/db"), "--meta"); !strings.Contains(out, "metadata: v2 (2 versions kept)") {
		t.Errorf("get --meta table:\n%s", out)
	}
	if out := e.ok("get", e.s("app/db"), "-o", "yaml"); !strings.Contains(out, "user: app") {
		t.Errorf("get yaml:\n%s", out)
	}
	if got := e.ok("get", e.v1("team/a"), "-f", "apiKey"); got != "v" {
		t.Errorf("get kv1 = %q", got)
	}

	e.fails("has no key", "get", e.s("app/db"), "nope")
	e.fails("has no key", "get", e.s("app/db"), "-f", "nope")
	e.fails("not found", "get", e.s("missing"))
	e.fails("deleted or destroyed", "get", e.s("gone"))
	e.fails("need a KV v2 mount", "get", e.v1("team/a@1"))
	e.fails("invalid version", "get", e.s("app/db@0"))
	e.fails("kvx only works on KV mounts", "get", "cubbyhole/x")
	e.fails("empty path", "get", "/")
}

func TestIntegrationSearch(t *testing.T) {
	e := integration(t)
	e.seed()

	type m struct{ Path, Key, In, Value string }
	var found []m
	e.json(&found, "search", e.s(""), "db")
	ins := map[string]int{}
	for _, f := range found {
		ins[f.In]++
	}
	if ins["path"] != 1 || ins["value"] != 1 { // path app/db, value db-old.internal
		t.Errorf("search db = %+v", found)
	}

	e.json(&found, "search", e.s(""), "-k", "-i", "-E", "^api_?key$")
	if len(found) != 1 || found[0].Key != "API_KEY" || strings.Contains(found[0].Value, "dup") {
		t.Errorf("search -k -i -E = %+v", found)
	}
	e.json(&found, "search", e.s(""), "-v", "dup-value", "-S")
	if len(found) != 2 || found[0].Value != "dup-value-123456" {
		t.Errorf("search -v -S = %+v", found)
	}
	e.json(&found, "search", e.v1(""), "-k", "apikey", "-i")
	if len(found) != 1 {
		t.Errorf("search kv1 = %+v", found)
	}

	out := e.ok("search", e.s(""), "-v", "dup-value", "-l")
	if out != e.s("app/api")+"\n"+e.s("other/copy")+"\n" {
		t.Errorf("search -l = %q", out)
	}
	var paths []string
	e.json(&paths, "search", e.s(""), "-p", "copy", "-l")
	if len(paths) != 1 {
		t.Errorf("search -p -l json = %v", paths)
	}
	if out := e.ok("search", e.s(""), "-k", "timeout"); !strings.Contains(out, "PATH") || !strings.Contains(out, "timeout") {
		t.Errorf("search table:\n%s", out)
	}

	_, _, err := e.run("", "search", e.s(""), "no-such-thing")
	if exitCode(err) != 1 {
		t.Errorf("search without match: exit %d (%v), want 1", exitCode(err), err)
	}
	_, _, err = e.run("", "search", e.s(""), "no-such-thing", "-l")
	if exitCode(err) != 1 {
		t.Errorf("search -l without match: exit %d, want 1", exitCode(err))
	}
	e.fails("invalid regex", "search", e.s(""), "(", "-E")
}

func TestIntegrationDiff(t *testing.T) {
	e := integration(t)
	e.seed()

	type change struct {
		Path, Status string
		Keys         []struct{ Key, Status, From, To string }
	}
	var ch []change
	e.json(&ch, "diff", e.s("app/db@1"), "-S")
	if len(ch) != 1 || ch[0].Path != "." || len(ch[0].Keys) != 1 || ch[0].Keys[0].From != "pw-one-long" || ch[0].Keys[0].To != "pw-two-long" {
		t.Errorf("diff @1 = %+v", ch)
	}
	out := e.ok("diff", e.s("app/db@1"), e.s("app/db@2"))
	for _, want := range []string{"--- " + e.s("app/db@1"), "+++ " + e.s("app/db@2"), "~ password: ***"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff versions missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "pw-") {
		t.Errorf("diff must mask values:\n%s", out)
	}

	// Trees: copy app/ to app2/, then change it.
	e.put(e.s("app2/db"), map[string]any{"user": "app", "password": "other", "port": "5432"})
	e.put(e.s("app2/new"), map[string]any{"a": "b"})
	e.json(&ch, "diff", e.s("app/"), e.s("app2/"))
	status := map[string]string{}
	for _, c := range ch {
		status[c.Path] = c.Status
	}
	if jsonOf(status) != `{"api":"removed","cache":"removed","db":"changed","new":"added"}` {
		t.Errorf("tree diff = %s", jsonOf(status))
	}
	out = e.ok("diff", e.s("app/"), e.s("app2/"))
	for _, want := range []string{"+ new", "- api", "~ db", "    + port = ***", "    - host = ***"} {
		if !strings.Contains(out, want) {
			t.Errorf("tree diff missing %q:\n%s", want, out)
		}
	}
	e.json(&ch, "diff", e.s("app/db"), e.s("app2/db"), "--keys-only")
	if len(ch) != 1 || len(ch[0].Keys) != 2 { // +port -host, password change ignored
		t.Errorf("diff --keys-only = %+v", ch)
	}

	// Identical sides, exit codes, second vault.
	if out := e.ok("diff", e.s("app/"), e.s("app/"), "--exit-code"); !strings.Contains(out, "no differences") {
		t.Errorf("identical diff:\n%s", out)
	}
	if _, _, err := e.run("", "diff", e.s("app/"), e.s("app2/"), "--exit-code"); exitCode(err) != 1 {
		t.Errorf("diff --exit-code: exit %d, want 1", exitCode(err))
	}
	if _, _, err := e.run("", "diff", e.s("app/"), e.s("app2/"), "--exit-code", "-o", "json"); exitCode(err) != 1 {
		t.Errorf("diff --exit-code json: exit %d, want 1", exitCode(err))
	}
	e.json(&ch, "diff", e.s("app/"), e.s("app/"), "--to-address", os.Getenv("VAULT_ADDR"))
	if len(ch) != 0 {
		t.Errorf("diff --to-address same vault = %+v", ch)
	}
	e.json(&ch, "diff", e.s("other/"), e.v1("team/"))
	if len(ch) != 2 {
		t.Errorf("diff v2 vs v1 = %+v", ch)
	}

	e.fails("give a version", "diff", e.s("app/db"))
	e.fails("not found", "diff", e.s("app/db@9"), e.s("app/db"))
	e.fails("127.0.0.1:1", "diff", e.s("app/"), e.s("app/"), "--to-address", "http://127.0.0.1:1")
}

func TestIntegrationAudit(t *testing.T) {
	e := integration(t)
	e.seed()

	type f struct{ Check, Path, Key, Detail string }
	var got []f
	e.json(&got, "audit", e.s(""), "-c", "empty,duplicate,deleted")
	want := []f{
		{"empty", e.s("app/cache"), "empty", "empty value"},
		{"duplicate", e.s("app/api"), "API_KEY", ""},
		{"duplicate", e.s("other/copy"), "key", ""},
		{"deleted", e.s("gone"), "", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("audit = %+v", got)
	}
	for i := range want {
		if got[i].Check != want[i].Check || got[i].Path != want[i].Path || got[i].Key != want[i].Key {
			t.Errorf("finding %d = %+v, want %+v", i, got[i], want[i])
		}
		if strings.Contains(got[i].Detail, "dup-value") {
			t.Errorf("audit leaked a value: %s", got[i].Detail)
		}
	}

	e.json(&got, "audit", e.s(""), "-c", "stale", "--older-than", "0d")
	if len(got) != 4 { // every readable secret, not the deleted one
		t.Errorf("audit stale 0d = %+v", got)
	}
	e.json(&got, "audit", e.s(""), "-c", "stale", "--older-than", "30d")
	if len(got) != 0 {
		t.Errorf("audit stale 30d = %+v", got)
	}
	e.json(&got, "audit", e.s(""), "-c", "versions", "--max-versions", "1")
	if len(got) != 1 || got[0].Path != e.s("app/db") {
		t.Errorf("audit versions = %+v", got)
	}
	e.json(&got, "audit", e.s(""), "-c", "naming", "--key-pattern", "^[a-z_]+$")
	if len(got) != 1 || got[0].Key != "API_KEY" {
		t.Errorf("audit naming = %+v", got)
	}
	e.json(&got, "audit", e.s(""), "-c", "duplicate", "--min-duplicate-length", "100")
	if len(got) != 0 {
		t.Errorf("audit duplicate min length = %+v", got)
	}
	e.json(&got, "audit", e.v1(""), "-c", "empty,stale,naming", "--key-pattern", "^[a-z]+$")
	if len(got) != 1 || got[0].Check != "naming" { // apiKey; stale needs v2 metadata
		t.Errorf("audit kv1 = %+v", got)
	}

	out := e.ok("audit", e.s(""))
	if !strings.Contains(out, "CHECK") || !strings.Contains(out, "findings in 5 secrets") {
		t.Errorf("audit table:\n%s", out)
	}
	if out := e.ok("audit", e.s("other"), "-c", "empty"); !strings.Contains(out, "1 secret checked, no findings") {
		t.Errorf("audit clean:\n%s", out)
	}
	if _, _, err := e.run("", "audit", e.s(""), "--exit-code"); exitCode(err) != 1 {
		t.Errorf("audit --exit-code: exit %d", exitCode(err))
	}
	if _, _, err := e.run("", "audit", e.s(""), "--exit-code", "-o", "json"); exitCode(err) != 1 {
		t.Errorf("audit --exit-code json: exit %d", exitCode(err))
	}
	if _, _, err := e.run("", "audit", e.s("other"), "-c", "empty", "--exit-code"); err != nil {
		t.Errorf("audit clean --exit-code: %v", err)
	}
	e.fails("unknown check", "audit", e.s(""), "-c", "nope")
	e.fails("--older-than", "audit", e.s(""), "--older-than", "soon")
	e.fails("--key-pattern", "audit", e.s(""), "--key-pattern", "(")
}

func TestIntegrationWhoami(t *testing.T) {
	e := integration(t)
	var info struct {
		Address  string
		Policies []string
		KVMounts []struct{ Path, Version string } `json:"kv_mounts"`
	}
	e.json(&info, "whoami")
	if info.Address != os.Getenv("VAULT_ADDR") || jsonOf(info.Policies) != `["root"]` {
		t.Errorf("whoami = %+v", info)
	}
	if jsonOf(info.KVMounts) != `[{"Path":"kv1/","Version":"1"},{"Path":"secret/","Version":"2"}]` {
		t.Errorf("whoami mounts = %s", jsonOf(info.KVMounts))
	}
	if out := e.ok("doctor"); !strings.Contains(out, "never expires") || !strings.Contains(out, "secret/ (v2)") {
		t.Errorf("doctor:\n%s", out)
	}
}

func TestIntegrationGlobalErrors(t *testing.T) {
	e := integration(t)
	e.fails("unknown output format", "ls", "secret/", "-o", "xml")
	e.fails("context deadline exceeded", "ls", "-r", "secret/", "--timeout", "1ns")
	e.fails("not implemented yet", "export", "secret/")
	if out, _, _ := e.run("", "--version"); !strings.Contains(out, "dev") {
		t.Errorf("--version = %q", out)
	}

	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("HOME", t.TempDir()) // no ~/.vault-token
	e.fails("no vault token", "ls", "secret/")

	t.Setenv("VAULT_TOKEN", "bad-token")
	e.fails("permission denied", "whoami")

	t.Setenv("VAULT_ADDR", "http://127.0.0.1:1")
	e.fails("detecting mount", "ls", "secret/")
	if (ExitError{Code: 3}).Error() != "exit status 3" {
		t.Error("ExitError.Error")
	}
}
