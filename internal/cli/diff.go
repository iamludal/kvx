package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamludal/kvx/internal/output"
	"github.com/iamludal/kvx/internal/vault"
)

type keyChange struct {
	Key    string `json:"key" yaml:"key"`
	Status string `json:"status" yaml:"status"` // added, removed, changed
	From   string `json:"from,omitempty" yaml:"from,omitempty"`
	To     string `json:"to,omitempty" yaml:"to,omitempty"`
}

type pathChange struct {
	Path   string      `json:"path" yaml:"path"`
	Status string      `json:"status" yaml:"status"` // added, removed, changed
	Keys   []keyChange `json:"keys,omitempty" yaml:"keys,omitempty"`
}

type side struct {
	ref  vault.Ref
	data map[string]map[string]any // relative path -> data
}

func newDiffCmd(a *app) *cobra.Command {
	var keysOnly, exitCode bool
	var toAddr, toNS, toToken string
	var wf walkFlags
	cmd := &cobra.Command{
		Use:   "diff <a> [b]",
		Short: "Compare two secrets, two trees, two versions or two clusters",
		Long: `Compare secrets key by key. Each side can be a secret, a directory, or a
KV v2 version (path@N). With a single path@N, it is compared to the latest
version. With --to-address, the second side is read from another Vault (e.g.
check a migration, or staging against prod).

Values are compared by content and shown masked unless --show-secrets.`,
		Example: `  kvx diff secret/app/db@3                       # v3 vs latest
  kvx diff secret/app/db@3 secret/app/db@5
  kvx diff secret/staging/ secret/prod/ --keys-only  # same keys everywhere?
  kvx diff secret/app/ secret/app/ --to-address https://vault.dr:8200
  kvx diff secret/a/ secret/b/ --exit-code && echo in sync`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			if len(args) == 1 {
				if !strings.Contains(args[0], "@") {
					return fmt.Errorf("with one argument, give a version to compare to latest: %s@N", args[0])
				}
				args = append(args, args[0][:strings.LastIndex(args[0], "@")])
			}
			left, err := a.loadSide(ctx, a.client, args[0], wf)
			if err != nil {
				return err
			}
			rc := a.client
			if toAddr != "" || toNS != "" || toToken != "" {
				rc, err = vault.NewClient(vault.Options{Address: orDefault(toAddr, a.g.address), Token: orDefault(toToken, a.g.token), Namespace: orDefault(toNS, a.g.namespace), KVVersion: a.g.kvVersion})
				if err != nil {
					return fmt.Errorf("second vault: %w", err)
				}
			}
			right, err := a.loadSide(ctx, rc, args[1], wf)
			if err != nil {
				return err
			}

			changes := diffSides(a, left, right, keysOnly)
			if ok, err := a.out.Structured(changes); ok {
				if err != nil {
					return err
				}
				return exitIf(exitCode && len(changes) > 0, 1)
			}
			fmt.Fprintf(a.out.W, "%s %s\n%s %s\n", a.out.Red("---"), left.ref, a.out.Green("+++"), right.ref)
			if len(changes) == 0 {
				fmt.Fprintln(a.out.W, a.out.Dim("no differences"))
			}
			for _, c := range changes {
				switch c.Status {
				case "added":
					fmt.Fprintln(a.out.W, a.out.Green("+ "+c.Path))
				case "removed":
					fmt.Fprintln(a.out.W, a.out.Red("- "+c.Path))
				default:
					fmt.Fprintln(a.out.W, a.out.Yellow("~ "+c.Path))
				}
				for _, k := range c.Keys {
					switch k.Status {
					case "added":
						fmt.Fprintln(a.out.W, a.out.Green("    + "+k.Key+suffix(" = ", k.To)))
					case "removed":
						fmt.Fprintln(a.out.W, a.out.Red("    - "+k.Key+suffix(" = ", k.From)))
					default:
						fmt.Fprintf(a.out.W, "    %s %s: %s → %s\n", a.out.Yellow("~"), k.Key, a.out.Red(k.From), a.out.Green(k.To))
					}
				}
			}
			return exitIf(exitCode && len(changes) > 0, 1)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&keysOnly, "keys-only", false, "compare key names only, ignore values")
	f.BoolVar(&exitCode, "exit-code", false, "exit 1 when there are differences (for CI)")
	f.StringVar(&toAddr, "to-address", "", "read the second side from this Vault address")
	f.StringVar(&toNS, "to-namespace", "", "namespace for the second side")
	f.StringVar(&toToken, "to-token", "", "token for the second side (default: same as the first)")
	wf.register(cmd)
	return cmd
}

func (a *app) loadSide(ctx context.Context, c *vault.Client, path string, wf walkFlags) (side, error) {
	if c == nil {
		var err error
		if c, err = a.vault(); err != nil {
			return side{}, err
		}
	}
	ref, err := c.Resolve(ctx, path)
	if err != nil {
		return side{}, err
	}
	s := side{ref: ref, data: map[string]map[string]any{}}
	if ref.Version > 0 || !ref.IsDir {
		// A single secret, unless it turns out to be a directory.
		if d, err := ref.KV.Read(ctx, ref.Path, ref.Version); err == nil {
			s.data[""] = d
			return s, nil
		} else if ref.Version > 0 {
			return side{}, err
		}
	}
	paths, err := ref.KV.Walk(ctx, ref.Path, wf.options(a))
	if err != nil {
		return side{}, err
	}
	all, err := ref.KV.ReadAll(ctx, paths, a.g.concurrency)
	if err != nil {
		return side{}, err
	}
	for p, d := range all {
		s.data[strings.TrimPrefix(strings.TrimPrefix(p, ref.Path), "/")] = d
	}
	return s, nil
}

func diffSides(a *app, l, r side, keysOnly bool) []pathChange {
	paths := map[string]bool{}
	for p := range l.data {
		paths[p] = true
	}
	for p := range r.data {
		paths[p] = true
	}
	var out []pathChange
	for _, p := range output.SortedKeys(paths) {
		ld, lok := l.data[p]
		rd, rok := r.data[p]
		name := p
		if name == "" {
			name = "."
		}
		switch {
		case !lok:
			out = append(out, pathChange{Path: name, Status: "added"})
		case !rok:
			out = append(out, pathChange{Path: name, Status: "removed"})
		default:
			if ks := diffKeys(a, ld, rd, keysOnly); len(ks) > 0 {
				out = append(out, pathChange{Path: name, Status: "changed", Keys: ks})
			}
		}
	}
	return out
}

func diffKeys(a *app, l, r map[string]any, keysOnly bool) []keyChange {
	keys := map[string]bool{}
	for k := range l {
		keys[k] = true
	}
	for k := range r {
		keys[k] = true
	}
	var out []keyChange
	for _, k := range output.SortedKeys(keys) {
		lv, lok := l[k]
		rv, rok := r[k]
		switch {
		case !lok:
			out = append(out, keyChange{Key: k, Status: "added", To: valueIf(a, rv, !keysOnly)})
		case !rok:
			out = append(out, keyChange{Key: k, Status: "removed", From: valueIf(a, lv, !keysOnly)})
		case !keysOnly && output.Stringify(lv) != output.Stringify(rv):
			out = append(out, keyChange{Key: k, Status: "changed", From: a.out.Value(lv), To: a.out.Value(rv)})
		}
	}
	return out
}

func valueIf(a *app, v any, show bool) string {
	if !show {
		return ""
	}
	return a.out.Value(v)
}

func suffix(sep, s string) string {
	if s == "" {
		return ""
	}
	return sep + s
}

func orDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
