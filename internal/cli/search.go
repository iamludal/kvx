package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamludal/kvx/internal/output"
)

type match struct {
	Path  string `json:"path" yaml:"path"`
	Key   string `json:"key,omitempty" yaml:"key,omitempty"`
	In    string `json:"in" yaml:"in"` // path, key or value
	Value string `json:"value,omitempty" yaml:"value,omitempty"`
}

func newSearchCmd(a *app) *cobra.Command {
	var inPaths, inKeys, inValues, useRegex, ignoreCase, filesOnly bool
	var wf walkFlags
	cmd := &cobra.Command{
		Use:     "search <path> <pattern>",
		Aliases: []string{"grep"},
		Short:   "Find secrets whose path, keys or values match a pattern",
		Long: `Search a tree for a substring (or a regular expression with -E) in secret
paths, key names and values. With no scope flag, all three are searched.

Values are matched in memory and printed masked unless --show-secrets, so you
can look for a leaked password without printing it.`,
		Example: `  kvx search secret/ postgres                  # anywhere
  kvx search secret/ -k -i 'api_?key' -E        # key names only, regex, case-insensitive
  kvx search secret/ -v 'hunter2' -l            # which secrets hold this value?
  kvx search secret/ -v -E '^https?://.*staging' -S`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			if !inPaths && !inKeys && !inValues {
				inPaths, inKeys, inValues = true, true, true
			}
			matchFn, err := matcher(args[1], useRegex, ignoreCase)
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

			var found []match
			if inPaths {
				for _, p := range paths {
					if matchFn(p) {
						found = append(found, match{Path: ref.KV.Full(p), In: "path"})
					}
				}
			}
			if inKeys || inValues {
				data, err := ref.KV.ReadAll(ctx, paths, a.g.concurrency)
				if err != nil {
					return err
				}
				for _, p := range paths {
					d := data[p]
					for _, k := range output.SortedKeys(d) {
						v := output.Stringify(d[k])
						switch {
						case inKeys && matchFn(k):
							found = append(found, match{Path: ref.KV.Full(p), Key: k, In: "key", Value: a.out.Value(v)})
						case inValues && matchFn(v):
							found = append(found, match{Path: ref.KV.Full(p), Key: k, In: "value", Value: a.out.Value(v)})
						}
					}
				}
			}

			if filesOnly {
				seen := map[string]bool{}
				var uniq []string
				for _, m := range found {
					if !seen[m.Path] {
						seen[m.Path] = true
						uniq = append(uniq, m.Path)
					}
				}
				if ok, err := a.out.Structured(uniq); ok {
					return err
				}
				for _, p := range uniq {
					fmt.Fprintln(a.out.W, p)
				}
				return exitIf(len(uniq) == 0, 1)
			}
			if ok, err := a.out.Structured(found); ok {
				return err
			}
			rows := make([][]string, 0, len(found))
			for _, m := range found {
				rows = append(rows, []string{m.Path, a.out.Yellow(orDash(m.Key)), a.out.Dim(m.In), m.Value})
			}
			if len(rows) > 0 {
				if err := a.out.Table([]string{"PATH", "KEY", "MATCH", "VALUE"}, rows); err != nil {
					return err
				}
			}
			// Like grep: exit 1 when nothing matched.
			return exitIf(len(found) == 0, 1)
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&inPaths, "paths", "p", false, "search secret paths")
	f.BoolVarP(&inKeys, "keys", "k", false, "search key names")
	f.BoolVarP(&inValues, "values", "v", false, "search values")
	f.BoolVarP(&useRegex, "regex", "E", false, "pattern is a regular expression")
	f.BoolVarP(&ignoreCase, "ignore-case", "i", false, "case-insensitive match")
	f.BoolVarP(&filesOnly, "files-with-matches", "l", false, "print only the paths of matching secrets")
	wf.register(cmd)
	return cmd
}

func matcher(pattern string, useRegex, ignoreCase bool) (func(string) bool, error) {
	if useRegex {
		if ignoreCase {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
		return re.MatchString, nil
	}
	if ignoreCase {
		p := strings.ToLower(pattern)
		return func(s string) bool { return strings.Contains(strings.ToLower(s), p) }, nil
	}
	return func(s string) bool { return strings.Contains(s, pattern) }, nil
}

func exitIf(cond bool, code int) error {
	if cond {
		return ExitError{Code: code}
	}
	return nil
}
