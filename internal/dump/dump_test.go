package dump

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"golang.org/x/crypto/ssh"
)

func tree() *Dump {
	return &Dump{
		Source:   "secret/app/",
		Exported: time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC),
		Secrets: map[string]Entry{
			"db":       {Data: map[string]any{"user": "app", "password": "p@ss w'rd"}, Metadata: &Metadata{CustomMetadata: map[string]string{"owner": "a"}, MaxVersions: 5}},
			"api/keys": {Data: map[string]any{"API_KEY": "k", "port": json.Number("8080"), "nested": map[string]any{"a": []any{json.Number("1"), "b"}}}},
		},
	}
}

func encode(t *testing.T, d *Dump, format string) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := Encode(&b, d, format); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestRoundTrip(t *testing.T) {
	for _, format := range []string{JSON, YAML} {
		t.Run(format, func(t *testing.T) {
			in := tree()
			b := encode(t, in, format)
			for _, hint := range []string{format, ""} {
				got, err := Decode(b, hint)
				if err != nil {
					t.Fatalf("decode (hint %q): %v\n%s", hint, err, b)
				}
				if got.Source != in.Source || !got.Exported.Equal(in.Exported) || got.Single() {
					t.Errorf("header = %q %v single=%v", got.Source, got.Exported, got.Single())
				}
				if !reflect.DeepEqual(got.Paths(), []string{"api/keys", "db"}) {
					t.Fatalf("paths = %v", got.Paths())
				}
				if s := asString(got.Secrets["api/keys"].Data["port"]); s != "8080" {
					t.Errorf("port = %q", s)
				}
				if s := asString(got.Secrets["api/keys"].Data["nested"]); s != `{"a":[1,"b"]}` {
					t.Errorf("nested = %s", s)
				}
				if m := got.Secrets["db"].Metadata; m == nil || m.CustomMetadata["owner"] != "a" || m.MaxVersions != 5 {
					t.Errorf("metadata = %+v", m)
				}
				if got.Secrets["db"].Data["password"] != "p@ss w'rd" {
					t.Errorf("db = %v", got.Secrets["db"].Data)
				}
			}
		})
	}
	if bytes.Contains(encode(t, tree(), YAML), []byte(`"8080"`)) {
		t.Error("yaml should write json.Number as a number")
	}
}

func TestSingle(t *testing.T) {
	d := &Dump{Source: "secret/app/db", Secrets: map[string]Entry{"": {Data: map[string]any{"user": "app"}}}}
	b := encode(t, d, JSON)
	if !bytes.Contains(b, []byte(`"secret": {`)) || bytes.Contains(b, []byte(`"secrets"`)) {
		t.Errorf("single secret layout:\n%s", b)
	}
	got, err := Decode(b, "")
	if err != nil || !got.Single() || got.Secrets[""].Data["user"] != "app" {
		t.Errorf("Decode single = %+v, %v", got, err)
	}
}

func TestDecodePlain(t *testing.T) {
	d, err := Decode([]byte(`{"db": {"user": "app"}, "/api/": {"k": "v"}}`), "")
	if err != nil || !reflect.DeepEqual(d.Paths(), []string{"api", "db"}) {
		t.Errorf("plain tree = %v, %v", d, err)
	}
	d, err = Decode([]byte("user: app\nport: 5432\n"), "")
	if err != nil || !d.Single() || d.Secrets[""].Data["port"] != 5432 {
		t.Errorf("plain single yaml = %+v, %v", d, err)
	}
	for in, want := range map[string]string{
		`{}`:                                "nothing to import",
		`{"../etc": {"a": "b"}}`:            "invalid secret path",
		`{"a//b": {"a": "b"}}`:              "invalid secret path",
		`{"a": {"x": "1"}, "a/": {"y": 2}}`: "appears twice",
		`{"kvx": 9, "secrets": {}}`:         "unsupported kvx export format 9",
		`{"kvx": 1}`:                        "holds no secret",
		`{"kvx": 1, "secrets": {"a": {}}}`:  `secret "a" has no data`,
		`{"kvx": 1, "secret": {}}`:          `"secret" has no data`,
		`{"kvx": 1, "secret": {"data": {}}, "secrets": {"a": {"data": {}}}}`: "both",
		`{"kvx": 1, "secrets": {"..": {"data": {}}}}`:                        "invalid secret path",
		`{not json`: "parsing JSON",
		"":          "no KEY=value",
	} {
		if _, err := Decode([]byte(in), ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Decode(%q) error = %v, want %q", in, err, want)
		}
	}
	if _, err := Decode([]byte("a: [unclosed"), YAML); err == nil || !strings.Contains(err.Error(), "parsing YAML") {
		t.Errorf("bad yaml: %v", err)
	}
	if _, err := Decode([]byte("{}"), "xml"); err == nil {
		t.Error("unknown format should fail")
	}
	if err := Encode(&bytes.Buffer{}, tree(), "xml"); err == nil {
		t.Error("Encode unknown format should fail")
	}
}

func TestFormatFromName(t *testing.T) {
	for name, want := range map[string]string{
		"a.json": JSON, "backup.yaml.age": YAML, "x.YML": YAML, "dir/.env": Dotenv,
		".env.local": Dotenv, "prod.env.asc": Dotenv, "data.txt": "", "-": "",
	} {
		if got := FormatFromName(name); got != want {
			t.Errorf("FormatFromName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestDotenv(t *testing.T) {
	values := map[string]any{
		"plain": "abc-123_./:@", "space": "a b", "quote": "it's", "multi": "l1\nl2 \"q\" $HOME \\ `x`",
		"empty": "", "num": json.Number("42"), "obj": map[string]any{"a": "b"}, "tab": "a\tb",
	}
	b := encode(t, &Dump{Secrets: map[string]Entry{"": {Data: values}}}, Dotenv)
	for _, want := range []string{"PLAIN=abc-123_./:@\n", "SPACE='a b'\n", `QUOTE="it's"`, "EMPTY=\n", "NUM=42\n", `OBJ='{"a":"b"}'`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("dotenv output lacks %q:\n%s", want, b)
		}
	}
	got, err := Decode(b, "")
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range values {
		if g := got.Secrets[""].Data[strings.ToUpper(k)]; g != asString(v) {
			t.Errorf("%s round trip = %q, want %q", k, g, asString(v))
		}
	}

	// Trees are prefixed; collisions and metadata are refused.
	b = encode(t, &Dump{Secrets: map[string]Entry{"db/main": {Data: map[string]any{"pass-word": "x"}}, "9x": {Data: map[string]any{"k": "v"}}}}, Dotenv)
	if string(b) != "_9X_K=v\nDB_MAIN_PASS_WORD=x\n" {
		t.Errorf("tree dotenv:\n%s", b)
	}
	var buf bytes.Buffer
	if err := Encode(&buf, &Dump{Secrets: map[string]Entry{"a": {Data: map[string]any{"b_c": 1}}, "a_b": {Data: map[string]any{"c": 2}}}}, Dotenv); err == nil || !strings.Contains(err.Error(), "both become A_B_C") {
		t.Errorf("collision: %v", err)
	}
	if err := Encode(&buf, tree(), Dotenv); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Errorf("metadata in dotenv: %v", err)
	}
}

func TestParseDotenv(t *testing.T) {
	in := "\ufeff# comment\r\n\nexport A=1\nB = bare value # trailing\nC='lit $X \\n'\nD=\"multi\nline\" # c\nE=\"\\q\"\nF=a#b\n"
	got, err := parseDotenv([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"A": "1", "B": "bare value", "C": `lit $X \n`, "D": "multi\nline", "E": `\q`, "F": "a#b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseDotenv = %#v", got)
	}
	for in, msg := range map[string]string{
		"just text":            "line 1: expected KEY=value",
		"A=1\n1B=2":            "line 2: expected KEY=value",
		"A='open":              "unterminated single quote",
		"A=\"open\nstill":      "unterminated double quote",
		"A=1\nB=\"x\ny\"\nA=2": "line 4: A is set twice",
	} {
		if _, err := parseDotenv([]byte(in)); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("parseDotenv(%q) = %v, want %q", in, err, msg)
		}
	}
}

func TestAge(t *testing.T) {
	dir := t.TempDir()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	idFile := filepath.Join(dir, "key.txt")
	os.WriteFile(idFile, []byte("# created by test\n"+id.String()+"\n"), 0o600)
	recFile := filepath.Join(dir, "recipients.txt")
	os.WriteFile(recFile, []byte("# team\n"+id.Recipient().String()+"\n"), 0o600)

	// SSH ed25519 key pair.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	sshFile := filepath.Join(dir, "id_ed25519")
	os.WriteFile(sshFile, pem.EncodeToMemory(block), 0o600)

	recs, err := ParseRecipients([]string{recFile, " ", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))})
	if err != nil || len(recs) != 2 {
		t.Fatalf("ParseRecipients = %v, %v", recs, err)
	}

	for _, armored := range []bool{false, true} {
		var b bytes.Buffer
		w, err := Encrypt(&b, recs, armored)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(`{"a": {"k": "v"}}`))
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if !IsEncrypted(b.Bytes()) || strings.HasPrefix(b.String(), "-----BEGIN") != armored {
			t.Fatalf("armored=%v: not detected as encrypted:\n%q", armored, b.String()[:30])
		}
		for _, key := range []string{idFile, sshFile} {
			plain, err := Decrypt(b.Bytes(), key)
			if err != nil || string(plain) != `{"a": {"k": "v"}}` {
				t.Errorf("Decrypt with %s (armored=%v) = %q, %v", filepath.Base(key), armored, plain, err)
			}
		}
	}
	if IsEncrypted([]byte(`{"kvx": 1}`)) {
		t.Error("plain JSON detected as encrypted")
	}

	// Errors.
	other, _ := age.GenerateX25519Identity()
	otherFile := filepath.Join(dir, "other.txt")
	os.WriteFile(otherFile, []byte(other.String()), 0o600)
	var b bytes.Buffer
	w, _ := Encrypt(&b, recs[:1], false)
	w.Close()
	if _, err := Decrypt(b.Bytes(), otherFile); err == nil || !strings.Contains(err.Error(), "does not hold a key") {
		t.Errorf("wrong key: %v", err)
	}
	if _, err := Decrypt(b.Bytes(), filepath.Join(dir, "missing")); err == nil {
		t.Error("missing identity file should fail")
	}
	if _, err := Decrypt([]byte("age-encryption.org/v1\ngarbage"), idFile); err == nil {
		t.Error("corrupt file should fail")
	}
	bad := filepath.Join(dir, "bad")
	os.WriteFile(bad, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nnope\n-----END OPENSSH PRIVATE KEY-----\n"), 0o600)
	if _, err := Decrypt(b.Bytes(), bad); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("bad ssh key: %v", err)
	}
	if _, err := Decrypt(b.Bytes(), recFile); err == nil {
		t.Error("a recipients file is not an identity file")
	}
	for _, in := range [][]string{{}, {"age1notakey"}, {"ssh-ed25519 AAAA"}, {filepath.Join(dir, "nope")}, {idFile}, {bad}} {
		if _, err := ParseRecipients(in); err == nil {
			t.Errorf("ParseRecipients(%v) should fail", in)
		} else if strings.Contains(err.Error(), id.String()) {
			t.Errorf("error leaks the secret key: %v", err)
		}
	}
	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, []byte("# nothing\n"), 0o600)
	if _, err := ParseRecipients([]string{empty}); err == nil || !strings.Contains(err.Error(), "no recipient") {
		t.Errorf("empty recipients file: %v", err)
	}
}
