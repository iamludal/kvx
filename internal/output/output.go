// Package output renders kvx results as tables, JSON or YAML, and masks
// secret values unless the user asked to see them.
package output

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// Format is an output format.
type Format string

const (
	Table Format = "table"
	JSON  Format = "json"
	YAML  Format = "yaml"
)

// ParseFormat validates a --output value.
func ParseFormat(s string) (Format, error) {
	switch f := Format(strings.ToLower(s)); f {
	case Table, JSON, YAML:
		return f, nil
	}
	return "", fmt.Errorf("unknown output format %q (want table, json or yaml)", s)
}

// Printer writes structured results.
type Printer struct {
	W           io.Writer
	Format      Format
	ShowSecrets bool
	Color       bool
}

// New returns a printer on w. Color is on only when w is a terminal and
// NO_COLOR is unset.
func New(w io.Writer, f Format, showSecrets, noColor bool) *Printer {
	color := false
	if file, ok := w.(*os.File); ok {
		color = !noColor && os.Getenv("NO_COLOR") == "" && term.IsTerminal(int(file.Fd()))
	}
	return &Printer{W: w, Format: f, ShowSecrets: showSecrets, Color: color}
}

// Structured writes v as JSON or YAML. It returns false for table format so
// the caller renders its own human view.
func (p *Printer) Structured(v any) (bool, error) {
	switch p.Format {
	case JSON:
		enc := json.NewEncoder(p.W)
		enc.SetIndent("", "  ")
		return true, enc.Encode(v)
	case YAML:
		enc := yaml.NewEncoder(p.W)
		enc.SetIndent(2)
		defer enc.Close()
		return true, enc.Encode(v)
	}
	return false, nil
}

// Table writes aligned columns.
//
// Columns are sized on what the terminal shows: color codes take no room
// (text/tabwriter counts their bytes, which misaligned colored columns).
func (p *Printer) Table(header []string, rows [][]string) error {
	all := rows
	if len(header) > 0 {
		h := make([]string, len(header))
		for i, c := range header {
			h[i] = p.Bold(c)
		}
		all = append([][]string{h}, rows...)
	}
	var widths []int
	for _, r := range all {
		for i, c := range r {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], visibleWidth(c))
		}
	}
	var b strings.Builder
	for _, r := range all {
		var line strings.Builder
		for i, c := range r {
			line.WriteString(c)
			if i < len(r)-1 {
				line.WriteString(strings.Repeat(" ", widths[i]-visibleWidth(c)+2))
			}
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteByte('\n')
	}
	_, err := io.WriteString(p.W, b.String())
	return err
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// visibleWidth is the number of characters s takes on screen, ignoring
// color codes.
func visibleWidth(s string) int { return utf8.RuneCountInString(ansi.ReplaceAllString(s, "")) }

// Value renders a secret value, masked unless ShowSecrets is set.
func (p *Printer) Value(v any) string {
	if p.ShowSecrets {
		return Stringify(v)
	}
	return Mask(v)
}

// Values masks every value of a secret unless ShowSecrets is set.
func (p *Printer) Values(d map[string]any) map[string]any {
	if p.ShowSecrets {
		return d
	}
	out := make(map[string]any, len(d))
	for k, v := range d {
		out[k] = Mask(v)
	}
	return out
}

// Mask hides a value but keeps a short fingerprint, so equal values can still
// be recognised across secrets without revealing them.
func Mask(v any) string {
	s := Stringify(v)
	if s == "" {
		return "<empty>"
	}
	return "***" + Fingerprint(s)[:6]
}

// Fingerprint is a stable hash of a value.
func Fingerprint(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Stringify renders any secret value as a string (nested values as JSON).
func Stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	case map[string]any, []any:
		b, _ := json.Marshal(t)
		return string(b)
	}
	return fmt.Sprint(v)
}

// Plural renders "1 secret", "3 secrets".
func Plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// SortedKeys returns map keys in order.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (p *Printer) paint(code, s string) string {
	if !p.Color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p *Printer) Bold(s string) string   { return p.paint("1", s) }
func (p *Printer) Dim(s string) string    { return p.paint("2", s) }
func (p *Printer) Red(s string) string    { return p.paint("31", s) }
func (p *Printer) Green(s string) string  { return p.paint("32", s) }
func (p *Printer) Yellow(s string) string { return p.paint("33", s) }
func (p *Printer) Blue(s string) string   { return p.paint("34", s) }
