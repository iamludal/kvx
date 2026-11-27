package dump

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

var (
	envInvalid = regexp.MustCompile(`[^A-Za-z0-9_]`)
	envName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	envBare    = regexp.MustCompile(`^[A-Za-z0-9_./:@%+,=-]*$`)
)

// EnvName turns a key (and, in a tree, its secret's relative path) into an
// environment variable name: upper case, anything but [A-Z0-9_] becomes "_".
// Same rule as `kvx get --env`.
func EnvName(rel, key string) string {
	name := key
	if rel != "" {
		name = rel + "_" + key
	}
	name = strings.ToUpper(envInvalid.ReplaceAllString(name, "_"))
	if name != "" && name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
}

// encodeDotenv flattens d into KEY=value lines. In a tree, names are
// prefixed with the secret's path (db/main + password -> DB_MAIN_PASSWORD).
// Metadata cannot be represented and is refused rather than dropped.
func encodeDotenv(w io.Writer, d *Dump) error {
	type line struct{ name, value, from string }
	var lines []line
	seen := map[string]string{}
	for _, p := range d.Paths() {
		e := d.Secrets[p]
		if e.Metadata != nil {
			return errors.New("the dotenv format cannot hold metadata: use json or yaml with --with-metadata")
		}
		keys := make([]string, 0, len(e.Data))
		for k := range e.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			name := EnvName(p, k)
			from := strings.TrimPrefix(p+"/"+k, "/")
			if prev, dup := seen[name]; dup {
				return fmt.Errorf("dotenv: %s and %s both become %s; export them separately or use json/yaml", prev, from, name)
			}
			seen[name] = from
			lines = append(lines, line{name, asString(e.Data[k]), from})
		}
	}
	bw := bufio.NewWriter(w)
	for _, l := range lines {
		fmt.Fprintf(bw, "%s=%s\n", l.name, quoteEnv(l.value))
	}
	return bw.Flush()
}

// quoteEnv quotes a value so that common .env readers (docker compose,
// dotenv libraries, `set -a; . ./.env`) and parseDotenv read it back as is.
func quoteEnv(v string) string {
	switch {
	case envBare.MatchString(v):
		return v
	case !strings.ContainsAny(v, "'\n\r"):
		return "'" + v + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`, "$", `\$`, "`", "\\`")
	return `"` + r.Replace(v) + `"`
}

// parseDotenv reads KEY=value lines: blank lines and # comments are
// ignored, an "export " prefix is allowed, values may be bare (trailing
// " # comment" removed), 'single-quoted' (literal) or "double-quoted"
// (with \n \t \r \" \\ \$ escapes, and possibly spanning lines).
func parseDotenv(b []byte) (map[string]any, error) {
	s := strings.TrimPrefix(strings.ReplaceAll(string(b), "\r\n", "\n"), "\ufeff")
	out := map[string]any{}
	lineNo := 0
	for pos := 0; pos < len(s); {
		lineNo++
		end := strings.IndexByte(s[pos:], '\n')
		if end < 0 {
			end = len(s)
		} else {
			end += pos
		}
		raw := s[pos:end]
		next := end + 1

		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			pos = next
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, rest, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || !envName.MatchString(name) {
			return nil, fmt.Errorf("dotenv line %d: expected KEY=value", lineNo)
		}
		rest = strings.TrimLeft(rest, " \t")
		var value string
		switch {
		case strings.HasPrefix(rest, "'"):
			closing := strings.IndexByte(rest[1:], '\'')
			if closing < 0 {
				return nil, fmt.Errorf("dotenv line %d: unterminated single quote", lineNo)
			}
			value = rest[1 : 1+closing]
		case strings.HasPrefix(rest, `"`):
			// The value may span lines: scan s itself from the opening quote.
			eq := strings.IndexByte(raw, '=') + 1
			open := pos + eq + strings.IndexByte(raw[eq:], '"')
			v, closeAt, err := unquoteDouble(s, open+1)
			if err != nil {
				return nil, fmt.Errorf("dotenv line %d: %w", lineNo, err)
			}
			value = v
			lineNo += strings.Count(s[pos:closeAt], "\n")
			if nl := strings.IndexByte(s[closeAt:], '\n'); nl >= 0 {
				next = closeAt + nl + 1
			} else {
				next = len(s)
			}
		default:
			if i := strings.Index(rest, " #"); i >= 0 {
				rest = rest[:i]
			}
			value = strings.TrimSpace(rest)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("dotenv line %d: %s is set twice", lineNo, name)
		}
		out[name] = value
		pos = next
	}
	return out, nil
}

// unquoteDouble reads a double-quoted value from s[i:] (just after the
// opening quote) and returns it with the index of the closing quote.
func unquoteDouble(s string, i int) (string, int, error) {
	var sb strings.Builder
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			return sb.String(), i, nil
		case c == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case '"', '\\', '$', '`':
				sb.WriteByte(s[i])
			default:
				sb.WriteByte('\\')
				sb.WriteByte(s[i])
			}
		default:
			sb.WriteByte(c)
		}
	}
	return "", 0, errors.New("unterminated double quote")
}
