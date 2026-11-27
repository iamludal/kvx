package cli

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/iamludal/kvx/internal/plan"
)

// replacer rewrites strings. A literal search is quoted into a regex so both
// modes share one code path; only the replacement syntax differs.
type replacer struct {
	re      *regexp.Regexp
	literal bool
	repl    string
}

func newReplacer(search, repl string, useRegex, ignoreCase bool) (*replacer, error) {
	if search == "" {
		return nil, fmt.Errorf("search pattern must not be empty")
	}
	pattern := search
	if !useRegex {
		pattern = regexp.QuoteMeta(search)
	}
	if ignoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %w", err)
	}
	return &replacer{re: re, literal: !useRegex, repl: repl}, nil
}

func (r *replacer) apply(s string) string {
	if r.literal {
		return r.re.ReplaceAllLiteralString(s, r.repl)
	}
	return r.re.ReplaceAllString(s, r.repl)
}

// change is one rewritten key or value, for the plan. Old and New are values
// (to be masked) for a value change and key names for a key change.
type change struct {
	Key      string
	Old, New string
	IsKey    bool
}

type rewrite struct {
	data       map[string]any
	changes    []change
	collisions []string // key names that would overwrite another key
}

// rewriteData applies r to the string values and/or key names of data,
// recursing into nested maps and lists. Non-string values are left alone, so
// replace never changes the type of a value. data is not modified.
func (r *replacer) rewriteData(data map[string]any, keys, values bool) rewrite {
	var rw rewrite
	rw.data = r.rewriteMap(data, "", keys, values, &rw)
	sort.Strings(rw.collisions)
	return rw
}

func (r *replacer) rewriteMap(m map[string]any, prefix string, keys, values bool, rw *rewrite) map[string]any {
	// Rename keys first so collisions are known before anything is written.
	names := make(map[string]string, len(m)) // old -> new
	taken := make(map[string]string, len(m)) // new -> old
	sorted := make([]string, 0, len(m))
	for k := range m {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		nk := k
		if keys {
			nk = r.apply(k)
		}
		names[k] = nk
		if other, dup := taken[nk]; dup && other != k {
			rw.collisions = append(rw.collisions, prefix+nk)
		}
		taken[nk] = k
	}
	// A renamed key landing on an untouched one is also a collision; the loop
	// above catches it because untouched keys map to themselves.

	out := make(map[string]any, len(m))
	for _, k := range sorted {
		nk, full := names[k], prefix+k
		if nk != k {
			rw.changes = append(rw.changes, change{Key: full, Old: k, New: nk, IsKey: true})
		}
		out[nk] = r.rewriteValue(m[k], prefix+nk, keys, values, rw)
	}
	return out
}

func (r *replacer) rewriteValue(v any, full string, keys, values bool, rw *rewrite) any {
	switch t := v.(type) {
	case string:
		if !values {
			return t
		}
		if n := r.apply(t); n != t {
			rw.changes = append(rw.changes, change{Key: full, Old: t, New: n})
			return n
		}
	case map[string]any:
		return r.rewriteMap(t, full+".", keys, values, rw)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = r.rewriteValue(e, fmt.Sprintf("%s[%d]", full, i), keys, values, rw)
		}
		return out
	}
	return v
}

func (a *app) describeChanges(cs []change) string {
	const max = 3
	var parts []string
	for i, c := range cs {
		if i == max {
			parts = append(parts, fmt.Sprintf("+%d more", len(cs)-max))
			break
		}
		if c.IsKey {
			parts = append(parts, fmt.Sprintf("key %s → %s", c.Old, c.New))
		} else {
			parts = append(parts, fmt.Sprintf("%s %s → %s", c.Key, a.out.Value(c.Old), a.out.Value(c.New)))
		}
	}
	return strings.Join(parts, "; ")
}

func newReplaceCmd(a *app) *cobra.Command {
	var m mutationFlags
	var wf walkFlags
	var useRegex, inKeys, inValues, ignoreCase bool
	cmd := &cobra.Command{
		Use:     "replace <path> <search> <replacement>",
		Aliases: []string{"sed"},
		Short:   "Search and replace in values (or key names) across a tree",
		Long: `Replace a substring (or a regular expression with -E) in the string values of
every secret under <path>, or in key names with --keys (or both).

Only strings are rewritten: numbers, booleans and null are never touched, and
nested maps and lists are searched. A secret whose renamed keys would collide
is skipped and reported. Values in the plan are masked unless --show-secrets.`,
		Example: `  kvx replace secret/ db-old.internal db-new.internal --values
  kvx replace secret/ -E 'postgres://([^@]+)@old' 'postgres://$1@new'
  kvx replace secret/ DB_PASS DB_PASSWORD --keys     # rename a key everywhere`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			if !inKeys && !inValues {
				inValues = true
			}
			r, err := newReplacer(args[1], args[2], useRegex, ignoreCase)
			if err != nil {
				return err
			}
			ref, err := a.resolve(ctx, args[0])
			if err != nil {
				return err
			}
			paths, err := ref.KV.Walk(ctx, ref.Path, wf.options(a))
			if err != nil {
				return err
			}

			var (
				mu      sync.Mutex
				ops     []plan.Op
				skipped []string
			)
			bar := a.progress.Start("Planning", len(paths))
			g, gctx := errgroup.WithContext(ctx)
			g.SetLimit(a.g.concurrency)
			for _, p := range paths {
				g.Go(func() error {
					defer bar.Add(1)
					st, err := ref.KV.Stat(gctx, p)
					if err != nil {
						return err
					}
					if !st.Exists || st.Data == nil {
						return nil
					}
					rw := r.rewriteData(st.Data, inKeys, inValues)
					if len(rw.changes) == 0 && len(rw.collisions) == 0 {
						return nil
					}
					full := ref.KV.Full(p)
					mu.Lock()
					defer mu.Unlock()
					if len(rw.collisions) > 0 {
						skipped = append(skipped, full+" (key collision: "+strings.Join(rw.collisions, ", ")+")")
						return nil
					}
					op := plan.Op{Kind: plan.Write, KV: ref.KV, Path: p, CAS: -1, Before: &st,
						Data: rw.data, Label: "update", Detail: a.describeChanges(rw.changes)}
					if ref.KV.Version == 2 {
						op.CAS = st.Version
					}
					ops = append(ops, op)
					return nil
				})
			}
			err = g.Wait()
			bar.Done()
			if err != nil {
				return err
			}
			return a.run(ctx, cmd, &m, ops, skipped)
		},
	}
	m.register(cmd)
	cmd.Flags().BoolVarP(&useRegex, "regex", "E", false, "search is a regular expression ($1 in replacement)")
	cmd.Flags().BoolVarP(&inKeys, "keys", "k", false, "replace in key names")
	cmd.Flags().BoolVarP(&inValues, "values", "v", false, "replace in values (default)")
	cmd.Flags().BoolVarP(&ignoreCase, "ignore-case", "i", false, "case-insensitive")
	wf.register(cmd)
	return cmd
}
