package cli

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/iamludal/kvx/internal/output"
	"github.com/iamludal/kvx/internal/vault"
)

// ---------------------------------------------------------------- ls

func newLsCmd(a *app) *cobra.Command {
	var long, recursive bool
	var wf walkFlags
	cmd := &cobra.Command{
		Use:   "ls <path>",
		Short: "List secrets and directories (-r for a flat recursive list)",
		Example: `  kvx ls secret/
  kvx ls -l secret/app            # with version and last update
  kvx ls -r secret/ | wc -l       # count every secret
  kvx ls -r secret/ --include '**/db*'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			ref, err := a.resolve(ctx, args[0])
			if err != nil {
				return err
			}
			var entries []string
			if recursive {
				paths, err := ref.KV.Walk(ctx, ref.Path, wf.options(a))
				if err != nil {
					return err
				}
				entries = paths
			} else {
				keys, err := ref.KV.List(ctx, ref.Path)
				if err != nil {
					return err
				}
				for _, k := range keys {
					entries = append(entries, strings.TrimPrefix(ref.Path+"/"+k, "/"))
				}
			}

			type entry struct {
				Path      string     `json:"path" yaml:"path"`
				Dir       bool       `json:"dir" yaml:"dir"`
				Version   int        `json:"version,omitempty" yaml:"version,omitempty"`
				UpdatedAt *time.Time `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
			}
			list := make([]entry, len(entries))
			for i, e := range entries {
				list[i] = entry{Path: ref.KV.Full(e), Dir: strings.HasSuffix(e, "/")}
			}
			if long && ref.KV.Version == 2 {
				bar := a.progress.Start("Reading metadata", len(list))
				g, gctx := errgroup.WithContext(ctx)
				g.SetLimit(a.g.concurrency)
				for i := range list {
					if list[i].Dir {
						bar.Add(1)
						continue
					}
					g.Go(func() error {
						defer bar.Add(1)
						m, err := ref.KV.Metadata(gctx, entries[i])
						if err != nil {
							return err
						}
						list[i].Version, list[i].UpdatedAt = m.CurrentVersion, &m.UpdatedTime
						return nil
					})
				}
				err := g.Wait()
				bar.Done()
				if err != nil {
					return err
				}
			}

			if ok, err := a.out.Structured(list); ok {
				return err
			}
			if !long {
				for _, e := range list {
					if e.Dir {
						fmt.Fprintln(a.out.W, a.out.Blue(e.Path))
					} else {
						fmt.Fprintln(a.out.W, e.Path)
					}
				}
				return nil
			}
			rows := make([][]string, 0, len(list))
			for _, e := range list {
				ver, upd := "-", "-"
				if e.Version > 0 {
					ver = "v" + strconv.Itoa(e.Version)
				}
				if e.UpdatedAt != nil {
					upd = e.UpdatedAt.Local().Format("2006-01-02 15:04") + " (" + humanAge(*e.UpdatedAt) + ")"
				}
				name := e.Path
				if e.Dir {
					name = a.out.Blue(name)
				}
				rows = append(rows, []string{ver, upd, name})
			}
			return a.out.Table([]string{"VERSION", "UPDATED", "PATH"}, rows)
		},
	}
	cmd.Flags().BoolVarP(&long, "long", "l", false, "show version and last update (KV v2)")
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "list every secret below path, one per line")
	wf.register(cmd)
	return cmd
}

// ---------------------------------------------------------------- tree

type treeNode struct {
	name     string
	children map[string]*treeNode
	secret   bool
	dir      bool // directory not expanded because of --depth
	deleted  bool // latest version deleted or destroyed (only known with --keys)
	data     map[string]any
}

func newTreeCmd(a *app) *cobra.Command {
	var keys, values, dirsOnly bool
	var wf walkFlags
	cmd := &cobra.Command{
		Use:   "tree <path>",
		Short: "Show secrets as a tree, optionally with their keys and values",
		Example: `  kvx tree secret/
  kvx tree secret/app --keys          # list each secret's keys
  kvx tree secret/app --values -S     # and their values, unmasked
  kvx tree secret/ -d 2 --dirs-only
  kvx tree secret/ -o json            # nested JSON, handy with jq`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			ref, err := a.resolve(ctx, args[0])
			if err != nil {
				return err
			}
			wo := wf.options(a)
			wo.KeepDirs = true
			all, err := ref.KV.Walk(ctx, ref.Path, wo)
			if err != nil {
				return err
			}
			var paths, cut []string // secrets, and directories cut by --depth
			for _, p := range all {
				if strings.HasSuffix(p, "/") {
					cut = append(cut, strings.TrimSuffix(p, "/"))
				} else {
					paths = append(paths, p)
				}
			}
			if values {
				keys = true
			}
			var data map[string]map[string]any
			if keys {
				if data, err = ref.KV.ReadAll(ctx, paths, a.g.concurrency, a.progress); err != nil {
					return err
				}
			}

			root := &treeNode{name: ref.KV.Full(ref.Path), children: map[string]*treeNode{}}
			for _, p := range append(append([]string{}, paths...), cut...) {
				isCut := !slices.Contains(paths, p)
				rel := strings.TrimPrefix(strings.TrimPrefix(p, ref.Path), "/")
				if rel == "" {
					rel = p
				}
				n := root
				parts := strings.Split(rel, "/")
				for i, part := range parts {
					child, ok := n.children[part]
					if !ok {
						child = &treeNode{name: part, children: map[string]*treeNode{}}
						n.children[part] = child
					}
					if i == len(parts)-1 && isCut {
						child.dir = true
					} else if i == len(parts)-1 {
						d, ok := data[p]
						child.secret, child.data, child.deleted = true, d, keys && !ok
					}
					n = child
				}
			}

			if a.out.Format != output.Table {
				_, err := a.out.Structured(treeToMap(a, root, keys, values))
				return err
			}
			fmt.Fprintln(a.out.W, a.out.Blue(root.name))
			dirs, secrets := printTree(a, root, "", keys, values, dirsOnly)
			fmt.Fprintf(a.out.W, "\n%s, %s\n", dirsLabel(dirs), output.Plural(secrets, "secret"))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&keys, "keys", "k", false, "show the keys of each secret")
	cmd.Flags().BoolVar(&values, "values", false, "show keys and values (masked unless --show-secrets)")
	cmd.Flags().BoolVar(&dirsOnly, "dirs-only", false, "show directories only")
	wf.register(cmd)
	return cmd
}

func dirsLabel(n int) string {
	if n == 1 {
		return "1 directory"
	}
	return fmt.Sprintf("%d directories", n)
}

func (n *treeNode) isDir() bool { return n.dir || len(n.children) > 0 }

func printTree(a *app, n *treeNode, prefix string, keys, values, dirsOnly bool) (dirs, secrets int) {
	names := output.SortedKeys(n.children)
	if dirsOnly {
		names = filter(names, func(s string) bool { return n.children[s].isDir() })
	}
	for i, name := range names {
		c := n.children[name]
		last := i == len(names)-1
		branch, indent := "├── ", "│   "
		if last {
			branch, indent = "└── ", "    "
		}
		isDir := c.isDir()
		label := name
		switch {
		case isDir && c.secret:
			label = a.out.Blue(name+"/") + a.out.Dim(" (also a secret)")
		case isDir:
			label = a.out.Blue(name + "/")
		}
		if c.deleted {
			label = a.out.Dim(name + " (deleted)")
		}
		fmt.Fprintln(a.out.W, prefix+branch+label)
		if c.secret {
			secrets++
			if keys && !dirsOnly {
				ks := output.SortedKeys(c.data)
				for j, k := range ks {
					kb := "├─ "
					if j == len(ks)-1 {
						kb = "└─ "
					}
					line := a.out.Dim(kb) + a.out.Yellow(k)
					if values {
						line += a.out.Dim(" = ") + a.out.Value(c.data[k])
					}
					fmt.Fprintln(a.out.W, prefix+indent+line)
				}
			}
		}
		if isDir {
			dirs++
			d, s := printTree(a, c, prefix+indent, keys, values, dirsOnly)
			dirs, secrets = dirs+d, secrets+s
		}
	}
	return dirs, secrets
}

func treeToMap(a *app, n *treeNode, keys, values bool) map[string]any {
	out := map[string]any{}
	for name, c := range n.children {
		var v any = map[string]any{}
		if c.secret {
			switch {
			case values:
				v = a.out.Values(c.data)
			case keys:
				v = output.SortedKeys(c.data)
			default:
				v = nil
			}
		}
		if c.isDir() {
			sub := treeToMap(a, c, keys, values)
			if c.secret {
				sub["_secret"] = v
			}
			v = sub
		}
		out[name] = v
	}
	return out
}

func filter(in []string, keep func(string) bool) []string {
	out := in[:0:0]
	for _, s := range in {
		if keep(s) {
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------- get

var envKeyRe = regexp.MustCompile(`[^A-Za-z0-9_]`)

func newGetCmd(a *app) *cobra.Command {
	var field string
	var env, meta, mask bool
	cmd := &cobra.Command{
		Use:   "get <path>[@version] [key...]",
		Short: "Read a secret (values are shown: you asked for them)",
		Example: `  kvx get secret/app/db
  kvx get secret/app/db password user
  kvx get secret/app/db -f password | pbcopy    # raw value, no newline
  kvx get secret/app/db@3 -o json               # an older version
  eval "$(kvx get secret/app/db --env)"         # export as env vars
  kvx get secret/app/db --meta                  # versions, dates, custom metadata`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			ref, err := a.resolve(ctx, args[0])
			if err != nil {
				return err
			}
			data, err := ref.KV.Read(ctx, ref.Path, ref.Version)
			if err != nil {
				return err
			}
			if want := args[1:]; len(want) > 0 {
				sub := make(map[string]any, len(want))
				for _, k := range want {
					v, ok := data[k]
					if !ok {
						return fmt.Errorf("%s has no key %q (keys: %s)", ref, k, strings.Join(output.SortedKeys(data), ", "))
					}
					sub[k] = v
				}
				data = sub
			}
			if field != "" {
				v, ok := data[field]
				if !ok {
					return fmt.Errorf("%s has no key %q (keys: %s)", ref, field, strings.Join(output.SortedKeys(data), ", "))
				}
				_, err := fmt.Fprint(a.out.W, output.Stringify(v))
				return err
			}
			show := func(v any) string {
				if mask {
					return output.Mask(v)
				}
				return output.Stringify(v)
			}
			if env {
				for _, k := range output.SortedKeys(data) {
					name := strings.ToUpper(envKeyRe.ReplaceAllString(k, "_"))
					fmt.Fprintf(a.out.W, "export %s=%s\n", name, shellQuote(show(data[k])))
				}
				return nil
			}

			var m *vault.Meta
			if meta {
				if m, err = ref.KV.Metadata(ctx, ref.Path); err != nil {
					return err
				}
			}
			if a.out.Format != output.Table {
				vals := data
				if mask {
					vals = make(map[string]any, len(data))
					for k, v := range data {
						vals[k] = output.Mask(v)
					}
				}
				if m == nil {
					_, err := a.out.Structured(vals)
					return err
				}
				_, err := a.out.Structured(map[string]any{"data": vals, "metadata": m})
				return err
			}
			rows := make([][]string, 0, len(data))
			for _, k := range output.SortedKeys(data) {
				rows = append(rows, []string{a.out.Yellow(k), show(data[k])})
			}
			if err := a.out.Table([]string{"KEY", "VALUE"}, rows); err != nil {
				return err
			}
			if m != nil {
				fmt.Fprintf(a.out.W, "\n%s v%d (%s kept), created %s, updated %s\n",
					a.out.Dim("metadata:"), m.CurrentVersion, output.Plural(m.Versions, "version"),
					humanAge(m.CreatedTime)+" ago", humanAge(m.UpdatedTime)+" ago")
				for _, k := range output.SortedKeys(m.CustomMetadata) {
					fmt.Fprintf(a.out.W, "  %s = %s\n", k, m.CustomMetadata[k])
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&field, "field", "f", "", "print only this key's raw value, without newline")
	cmd.Flags().BoolVar(&env, "env", false, "print as shell `export KEY=value` lines")
	cmd.Flags().BoolVar(&meta, "meta", false, "also show KV v2 metadata")
	cmd.Flags().BoolVar(&mask, "mask", false, "mask values (for screen sharing)")
	return cmd
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ---------------------------------------------------------------- whoami

func newWhoamiCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "whoami",
		Aliases: []string{"doctor"},
		Short:   "Show the address, token identity, policies, TTL and visible KV mounts",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			c, err := a.vault()
			if err != nil {
				return err
			}
			s, err := c.API.Auth().Token().LookupSelfWithContext(ctx)
			if err != nil {
				return fmt.Errorf("looking up token on %s: %w", c.Address(), err)
			}
			info := map[string]any{
				"address":      c.Address(),
				"namespace":    c.API.Namespace(),
				"display_name": s.Data["display_name"],
				"policies":     s.Data["policies"],
				"ttl":          s.Data["ttl"],
				"renewable":    s.Data["renewable"],
			}
			type mount struct {
				Path    string `json:"path" yaml:"path"`
				Version string `json:"version" yaml:"version"`
			}
			var mounts []mount
			if ms, err := c.API.Logical().ReadWithContext(ctx, "sys/internal/ui/mounts"); err == nil && ms != nil {
				if secret, ok := ms.Data["secret"].(map[string]any); ok {
					for p, raw := range secret {
						m, _ := raw.(map[string]any)
						if t := fmt.Sprint(m["type"]); t != "kv" && t != "generic" {
							continue
						}
						v := "1"
						if o, ok := m["options"].(map[string]any); ok && o["version"] != nil {
							v = fmt.Sprint(o["version"])
						}
						mounts = append(mounts, mount{Path: p, Version: v})
					}
				}
			}
			sort.Slice(mounts, func(i, j int) bool { return mounts[i].Path < mounts[j].Path })
			info["kv_mounts"] = mounts
			if ok, err := a.out.Structured(info); ok {
				return err
			}
			ttl := "never expires"
			if n, err := strconv.Atoi(fmt.Sprint(s.Data["ttl"])); err == nil && n > 0 {
				ttl = (time.Duration(n) * time.Second).String()
			}
			rows := [][]string{
				{"address", c.Address()},
				{"namespace", orDash(c.API.Namespace())},
				{"identity", fmt.Sprint(s.Data["display_name"])},
				{"policies", fmt.Sprint(s.Data["policies"])},
				{"ttl", ttl},
			}
			for _, m := range mounts {
				rows = append(rows, []string{"kv mount", m.Path + " (v" + m.Version + ")"})
			}
			return a.out.Table(nil, rows)
		},
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// humanAge renders how long ago t was: 45s, 3h, 12d, 2y.
func humanAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 730*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return fmt.Sprintf("%dy", int(d.Hours()/24/365))
}

// parseAge parses durations with day and week units: 90d, 2w, 12h.
func parseAge(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		v, err := strconv.Atoi(n)
		return time.Duration(v) * 24 * time.Hour, err
	}
	if n, ok := strings.CutSuffix(s, "w"); ok {
		v, err := strconv.Atoi(n)
		return time.Duration(v) * 7 * 24 * time.Hour, err
	}
	return time.ParseDuration(s)
}
