// Package dump reads and writes kvx exports: JSON, YAML or .env files holding
// one secret or a tree of secrets, optionally encrypted with age.
package dump

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// FormatVersion is the version of the JSON/YAML layout written by Encode.
const FormatVersion = 1

// Formats Encode and Decode understand.
const (
	JSON   = "json"
	YAML   = "yaml"
	Dotenv = "dotenv"
)

// Metadata is the KV v2 metadata an export can carry.
type Metadata struct {
	CustomMetadata map[string]string `json:"custom_metadata,omitempty" yaml:"custom_metadata,omitempty"`
	MaxVersions    int               `json:"max_versions,omitempty" yaml:"max_versions,omitempty"`
	CASRequired    bool              `json:"cas_required,omitempty" yaml:"cas_required,omitempty"`
}

// Entry is one secret.
type Entry struct {
	Data     map[string]any `json:"data" yaml:"data"`
	Metadata *Metadata      `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// Dump is a set of secrets keyed by their path relative to the exported
// root. A single secret (as opposed to a directory) is stored under "".
type Dump struct {
	Source   string // full path that was exported, informational
	Exported time.Time
	Secrets  map[string]Entry
}

// Single tells whether the dump holds one secret rather than a tree.
func (d *Dump) Single() bool {
	_, ok := d.Secrets[""]
	return ok && len(d.Secrets) == 1
}

// Paths returns the relative paths, sorted.
func (d *Dump) Paths() []string {
	out := make([]string, 0, len(d.Secrets))
	for p := range d.Secrets {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// wire is the on-disk layout of the JSON and YAML formats.
type wire struct {
	Kvx      int              `json:"kvx" yaml:"kvx"`
	Source   string           `json:"source,omitempty" yaml:"source,omitempty"`
	Exported *time.Time       `json:"exported,omitempty" yaml:"exported,omitempty"`
	Secret   *Entry           `json:"secret,omitempty" yaml:"secret,omitempty"`
	Secrets  map[string]Entry `json:"secrets,omitempty" yaml:"secrets,omitempty"`
}

// Encode writes d in the given format.
func Encode(w io.Writer, d *Dump, format string) error {
	if format == Dotenv {
		return encodeDotenv(w, d)
	}
	out := wire{Kvx: FormatVersion, Source: d.Source}
	if !d.Exported.IsZero() {
		t := d.Exported.UTC().Truncate(time.Second)
		out.Exported = &t
	}
	if d.Single() {
		e := d.Secrets[""]
		out.Secret = &e
	} else {
		out.Secrets = d.Secrets
	}
	switch format {
	case JSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(out)
	case YAML:
		for p, e := range out.Secrets {
			e.Data = yamlSafe(e.Data)
			out.Secrets[p] = e
		}
		if out.Secret != nil {
			out.Secret = &Entry{Data: yamlSafe(out.Secret.Data), Metadata: out.Secret.Metadata}
		}
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		if err := enc.Encode(out); err != nil {
			return err
		}
		return enc.Close()
	}
	return fmt.Errorf("unknown format %q: expected json, yaml or dotenv", format)
}

// yamlSafe turns json.Number (as the Vault client returns numbers) into
// real numbers, which YAML would otherwise write as quoted strings.
func yamlSafe(d map[string]any) map[string]any {
	out := make(map[string]any, len(d))
	for k, v := range d {
		out[k] = yamlValue(v)
	}
	return out
}

func yamlValue(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case map[string]any:
		return yamlSafe(t)
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = yamlValue(x)
		}
		return out
	}
	return v
}

// FormatFromName guesses the format from a file name ("" when unknown).
// Encryption suffixes (.age, .asc) are ignored: backup.yaml.age is YAML.
func FormatFromName(name string) string {
	name = strings.ToLower(name)
	for _, s := range []string{".age", ".asc"} {
		name = strings.TrimSuffix(name, s)
	}
	switch {
	case strings.HasSuffix(name, ".json"):
		return JSON
	case strings.HasSuffix(name, ".yaml"), strings.HasSuffix(name, ".yml"):
		return YAML
	case strings.HasSuffix(name, ".env"), path.Base(name) == ".env", strings.HasPrefix(path.Base(name), ".env."):
		return Dotenv
	}
	return ""
}

// Decode parses a kvx export, or any JSON/YAML/.env file:
//
//   - a kvx export (a "kvx" format version and "secret" or "secrets");
//   - a map of relative paths to key/value objects (a tree);
//   - a single key/value object, or a .env file (one secret).
//
// format "" detects the format from the content.
func Decode(b []byte, format string) (*Dump, error) {
	if format == "" {
		format = detect(b)
	}
	if format == Dotenv {
		data, err := parseDotenv(b)
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			return nil, errors.New("nothing to import: no KEY=value line found")
		}
		return &Dump{Secrets: map[string]Entry{"": {Data: data}}}, nil
	}

	var generic map[string]any
	if err := unmarshal(b, format, &generic); err != nil {
		return nil, err
	}
	if len(generic) == 0 {
		return nil, errors.New("nothing to import: the input is empty")
	}
	if _, ok := generic["kvx"]; ok {
		return decodeExport(b, format)
	}

	d := &Dump{Secrets: map[string]Entry{}}
	tree := true
	for _, v := range generic {
		if _, ok := v.(map[string]any); !ok {
			tree = false
			break
		}
	}
	if !tree {
		d.Secrets[""] = Entry{Data: generic}
		return d, nil
	}
	for p, v := range generic {
		if err := d.add(p, Entry{Data: v.(map[string]any)}); err != nil {
			return nil, err
		}
	}
	return d, nil
}

func decodeExport(b []byte, format string) (*Dump, error) {
	var in wire
	if err := unmarshal(b, format, &in); err != nil {
		return nil, err
	}
	if in.Kvx != FormatVersion {
		return nil, fmt.Errorf("unsupported kvx export format %d (this kvx reads format %d): upgrade kvx", in.Kvx, FormatVersion)
	}
	d := &Dump{Source: in.Source, Secrets: map[string]Entry{}}
	if in.Exported != nil {
		d.Exported = *in.Exported
	}
	switch {
	case in.Secret != nil && in.Secrets != nil:
		return nil, errors.New("invalid kvx export: both \"secret\" and \"secrets\" are set")
	case in.Secret != nil:
		if in.Secret.Data == nil {
			return nil, errors.New("invalid kvx export: \"secret\" has no data")
		}
		d.Secrets[""] = *in.Secret
	default:
		for p, e := range in.Secrets {
			if e.Data == nil {
				return nil, fmt.Errorf("invalid kvx export: secret %q has no data", p)
			}
			if err := d.add(p, e); err != nil {
				return nil, err
			}
		}
	}
	if len(d.Secrets) == 0 {
		return nil, errors.New("nothing to import: the export holds no secret")
	}
	return d, nil
}

// add stores e under a cleaned relative path, rejecting anything that could
// escape the import root.
func (d *Dump) add(p string, e Entry) error {
	clean := strings.Trim(p, "/")
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid secret path %q in the input", p)
		}
	}
	if _, dup := d.Secrets[clean]; dup {
		return fmt.Errorf("secret path %q appears twice in the input", clean)
	}
	d.Secrets[clean] = e
	return nil
}

func unmarshal(b []byte, format string, v any) error {
	switch format {
	case JSON:
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if err := dec.Decode(v); err != nil {
			return fmt.Errorf("parsing JSON: %w", err)
		}
		return nil
	case YAML:
		if err := yaml.Unmarshal(b, v); err != nil {
			return fmt.Errorf("parsing YAML: %w", err)
		}
		return nil
	}
	return fmt.Errorf("unknown format %q: expected json, yaml or dotenv", format)
}

// detect tells JSON, YAML and .env apart. JSON objects start with "{"; a
// YAML document that is a mapping is YAML; anything else (KEY=value lines
// are a plain YAML string) is .env.
func detect(b []byte) string {
	t := bytes.TrimSpace(b)
	if bytes.HasPrefix(t, []byte("{")) {
		return JSON
	}
	var m map[string]any
	if yaml.Unmarshal(t, &m) == nil && len(m) > 0 {
		return YAML
	}
	return Dotenv
}

// asString renders a secret value for a .env file (nested values as JSON).
func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	case map[string]any, []any:
		b, _ := json.Marshal(t)
		return string(b)
	}
	return fmt.Sprint(v)
}
