package cli

import (
	"context"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/iamludal/kvx/internal/output"
	"github.com/iamludal/kvx/internal/plan"
	"github.com/iamludal/kvx/internal/snapshot"
	"github.com/iamludal/kvx/internal/vault"
)

// Every command in this file builds a list of plan.Op and hands it to
// plan.Run, which prints the plan, asks for confirmation, snapshots the
// secrets about to change, and applies the ops.

type mutationFlags struct {
	dryRun   bool
	yes      bool
	noBackup bool
}

func (m *mutationFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&m.dryRun, "dry-run", false, "print the plan and exit without changing anything")
	cmd.Flags().BoolVarP(&m.yes, "yes", "y", false, "apply without asking for confirmation")
	cmd.Flags().BoolVar(&m.noBackup, "no-backup", false, "do not snapshot secrets before changing them (disables undo)")
}

func (a *app) run(ctx context.Context, cmd *cobra.Command, m *mutationFlags, ops []plan.Op, skipped []string) error {
	return plan.Run(ctx, a.out, ops, skipped, plan.Options{
		DryRun:      m.dryRun,
		Yes:         m.yes,
		NoBackup:    m.noBackup,
		Concurrency: a.g.concurrency,
		Command:     "kvx " + strings.Join(os.Args[1:], " "),
		In:          a.in,
		Err:         a.errW,
		Progress:    a.progress,
	})
}

// remoteFlags select another Vault for the destination side.
type remoteFlags struct{ address, namespace, token string }

func (r *remoteFlags) register(cmd *cobra.Command, what string) {
	cmd.Flags().StringVar(&r.address, "to-address", "", what+" another Vault address")
	cmd.Flags().StringVar(&r.namespace, "to-namespace", "", what+" another namespace")
	cmd.Flags().StringVar(&r.token, "to-token", "", "token for --to-address (default: same token; prefer $KVX_TO_TOKEN)")
}

func (a *app) remote(r remoteFlags) (*vault.Client, error) {
	if r.address == "" && r.namespace == "" && r.token == "" && os.Getenv("KVX_TO_TOKEN") == "" {
		return a.vault()
	}
	c, err := vault.NewClient(vault.Options{
		Address:   orDefault(r.address, a.g.address),
		Token:     orDefault(r.token, orDefault(os.Getenv("KVX_TO_TOKEN"), a.g.token)),
		Namespace: orDefault(r.namespace, a.g.namespace),
		KVVersion: a.g.kvVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("destination vault: %w", err)
	}
	return c, nil
}

// sources returns the secrets a src argument designates, and whether it is a
// single secret (as opposed to a directory).
func (a *app) sources(ctx context.Context, ref vault.Ref, wf walkFlags) ([]string, bool, error) {
	if !ref.IsDir && ref.Path != "" {
		st, err := ref.KV.Stat(ctx, ref.Path)
		if err != nil {
			return nil, false, err
		}
		if st.Exists {
			return []string{ref.Path}, true, nil
		}
	}
	paths, err := ref.KV.Walk(ctx, ref.Path, wf.options(a))
	if err != nil {
		return nil, false, err
	}
	if len(paths) == 0 {
		return nil, false, fmt.Errorf("nothing found at %s", ref)
	}
	return paths, false, nil
}

// deleteSources identifies a secret through LIST rather than reading its data.
func (a *app) deleteSources(ctx context.Context, ref vault.Ref, wf walkFlags) ([]string, bool, error) {
	if !ref.IsDir && ref.Path != "" {
		parent := path.Dir(ref.Path)
		if parent == "." {
			parent = ""
		}
		keys, err := ref.KV.List(ctx, parent)
		if err != nil {
			return nil, false, err
		}
		if slices.Contains(keys, path.Base(ref.Path)) {
			return []string{ref.Path}, true, nil
		}
	}
	opts := wf.options(a)
	opts.SkipRootCheck = true
	paths, err := ref.KV.Walk(ctx, ref.Path, opts)
	if err != nil {
		return nil, false, err
	}
	if len(paths) == 0 {
		return nil, false, fmt.Errorf("nothing found at %s", ref)
	}
	return paths, false, nil
}

func relUnder(root, p string) string {
	if root == "" {
		return p
	}
	return strings.TrimPrefix(p, root+"/")
}

func joinPath(dir, rel string) string {
	return strings.Trim(strings.TrimRight(dir, "/")+"/"+strings.TrimLeft(rel, "/"), "/")
}

func sameLocation(a, b *vault.KV, pa, pb string) bool {
	return a.Address() == b.Address() && a.Namespace() == b.Namespace() && a.Mount == b.Mount && pa == pb
}

// ---------------------------------------------------------------- cp / mv

type copyOptions struct {
	onConflict   string
	allVersions  bool
	withMetadata bool
	verify       bool
}

type copyPlan struct {
	ops     []plan.Op
	skipped []string
	// moved lists the sources whose data is now (or will be) at destination,
	// so mv can delete them.
	moved []string
}

func (a *app) planCopy(ctx context.Context, src, dst vault.Ref, wf walkFlags, o copyOptions) (*copyPlan, error) {
	if err := checkOnConflict(o.onConflict); err != nil {
		return nil, err
	}
	paths, single, err := a.sources(ctx, src, wf)
	if err != nil {
		return nil, err
	}
	target := func(p string) string {
		if single {
			if dst.IsDir || dst.Path == "" {
				return joinPath(dst.Path, path.Base(p))
			}
			return dst.Path
		}
		return joinPath(dst.Path, relUnder(src.Path, p))
	}

	var (
		mu        sync.Mutex
		cp        copyPlan
		conflicts []string
	)
	bar := a.progress.Start("Planning", len(paths))
	defer bar.Done()
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(a.g.concurrency)
	for _, p := range paths {
		to := target(p)
		if sameLocation(src.KV, dst.KV, p, to) {
			return nil, fmt.Errorf("%s: source and destination are the same", src.KV.Full(p))
		}
		g.Go(func() error {
			defer bar.Add(1)
			from, err := src.KV.Stat(gctx, p)
			if err != nil {
				return err
			}
			if from.Data == nil {
				mu.Lock()
				cp.skipped = append(cp.skipped, src.KV.Full(p)+" (latest version deleted)")
				mu.Unlock()
				return nil
			}
			cur, err := dst.KV.Stat(gctx, to)
			if err != nil {
				return err
			}
			op := plan.Op{Kind: plan.Write, KV: dst.KV, Path: to, CAS: -1, Verify: o.verify, Before: &cur, Data: from.Data}
			if dst.KV.Version == 2 {
				op.CAS = cur.Version
			}
			if o.withMetadata {
				op.Meta = from.Meta
			}
			detail := "from " + src.KV.Full(p)

			d := decideWrite(o.onConflict, cur, from.Data)
			if d.label == "create" && o.allVersions && src.KV.Version == 2 && dst.KV.Version == 2 {
				h, err := src.KV.History(gctx, p)
				if err != nil {
					return err
				}
				op.History = h
				detail += ", " + output.Plural(len(h), "version")
			}

			mu.Lock()
			defer mu.Unlock()
			switch {
			case d.conflict:
				conflicts = append(conflicts, dst.KV.Full(to))
				return nil
			case d.skip != "":
				cp.skipped = append(cp.skipped, dst.KV.Full(to)+" ("+d.skip+")")
				if d.same {
					cp.moved = append(cp.moved, p)
				}
				return nil
			}
			op.Label, op.Data, op.Detail = d.label, d.data, detail+d.changes
			cp.ops = append(cp.ops, op)
			cp.moved = append(cp.moved, p)
			return nil
		})
	}
	err = g.Wait()
	bar.Done()
	if err != nil {
		return nil, err
	}
	if err := conflictError(conflicts); err != nil {
		return nil, err
	}
	slices.Sort(cp.moved)
	return &cp, nil
}

func checkOnConflict(mode string) error {
	if !slices.Contains([]string{"fail", "skip", "overwrite", "merge"}, mode) {
		return fmt.Errorf("--on-conflict must be fail, skip, overwrite or merge, not %q", mode)
	}
	return nil
}

// writeDecision is what to do when data must land where cur stands.
type writeDecision struct {
	label    string         // create, overwrite or merge; "" when nothing is written
	data     map[string]any // what to write
	changes  string         // key-level summary for the plan, " (+1 ~2 keys)"
	skip     string         // why nothing is written
	same     bool           // the destination already holds the data
	conflict bool           // exists and differs, and --on-conflict is fail
}

// decideWrite applies --on-conflict (fail, skip, overwrite or merge, where
// the incoming keys win). Identical destinations are always skipped, so cp
// and import can be re-run.
func decideWrite(mode string, cur vault.State, data map[string]any) writeDecision {
	switch {
	case !cur.Exists:
		return writeDecision{label: "create", data: data}
	case cur.Data != nil && plan.Equal(cur.Data, data):
		return writeDecision{skip: "already identical", same: true}
	case mode == "skip":
		return writeDecision{skip: "exists"}
	case mode == "overwrite":
		return writeDecision{label: "overwrite", data: data, changes: describeKeyChanges(cur.Data, data)}
	case mode == "merge":
		merged := map[string]any{}
		for k, v := range cur.Data {
			merged[k] = v
		}
		for k, v := range data {
			merged[k] = v
		}
		if plan.Equal(merged, cur.Data) {
			return writeDecision{skip: "nothing to merge", same: true}
		}
		return writeDecision{label: "merge", data: merged, changes: describeKeyChanges(cur.Data, merged)}
	}
	return writeDecision{conflict: true}
}

func conflictError(conflicts []string) error {
	if len(conflicts) == 0 {
		return nil
	}
	slices.Sort(conflicts)
	n, more := len(conflicts), ""
	if n > 10 {
		more = fmt.Sprintf("\n  ... and %d more", n-10)
		conflicts = conflicts[:10]
	}
	return fmt.Errorf("%d destination secret(s) already exist and differ:\n  %s%s\nchoose --on-conflict skip, overwrite or merge",
		n, strings.Join(conflicts, "\n  "), more)
}

// describeKeyChanges summarises key-level changes without values: " (+2 ~1 -1 keys)".
func describeKeyChanges(before, after map[string]any) string {
	var add, chg, del int
	for k, v := range after {
		old, ok := before[k]
		switch {
		case !ok:
			add++
		case output.Stringify(old) != output.Stringify(v):
			chg++
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			del++
		}
	}
	var parts []string
	if add > 0 {
		parts = append(parts, fmt.Sprintf("+%d", add))
	}
	if chg > 0 {
		parts = append(parts, fmt.Sprintf("~%d", chg))
	}
	if del > 0 {
		parts = append(parts, fmt.Sprintf("-%d", del))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, " ") + " keys)"
}

func newCpCmd(a *app) *cobra.Command {
	var m mutationFlags
	var wf walkFlags
	var rf remoteFlags
	var o copyOptions
	cmd := &cobra.Command{
		Use:     "cp <src> <dst>",
		Aliases: []string{"copy", "clone"},
		Short:   "Copy a secret or a whole tree, across mounts, namespaces or clusters",
		Long: `Copy secrets like cp -r: a directory is copied with its structure under
dst, a single secret goes to dst (or into dst/ if dst ends with a slash).

Destinations that already exist and differ stop the copy unless you choose
--on-conflict. Identical destinations are skipped, so cp can be re-run.
Writes use check-and-set (KV v2): a secret changed between the plan and the
apply is not overwritten.`,
		Example: `  kvx cp secret/app/ secret/app-v2/
  kvx cp secret/app/db secret/archive/            # -> secret/archive/db
  kvx cp secret/app/ secret/app/ --to-address https://vault.dr:8200
  kvx cp secret/team-a/ kv1/team-a/ --on-conflict merge
  kvx cp secret/app/ secret/app-v2/ --all-versions --with-metadata`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			src, err := a.resolve(ctx, args[0])
			if err != nil {
				return err
			}
			dc, err := a.remote(rf)
			if err != nil {
				return err
			}
			dst, err := dc.Resolve(ctx, args[1])
			if err != nil {
				return err
			}
			cp, err := a.planCopy(ctx, src, dst, wf, o)
			if err != nil {
				return err
			}
			return a.run(ctx, cmd, &m, cp.ops, cp.skipped)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.onConflict, "on-conflict", "fail", "when dst exists and differs: fail, skip, overwrite or merge (keys, src wins)")
	f.BoolVar(&o.allVersions, "all-versions", false, "replay the full version history into new destinations (KV v2 -> v2)")
	f.BoolVar(&o.withMetadata, "with-metadata", false, "also copy custom_metadata, max_versions and cas_required")
	f.BoolVar(&o.verify, "verify", false, "read every written secret back and compare")
	rf.register(cmd, "write to")
	wf.register(cmd)
	m.register(cmd)
	return cmd
}

func newMvCmd(a *app) *cobra.Command {
	var m mutationFlags
	var wf walkFlags
	var rf remoteFlags
	var o copyOptions
	var keepHistory bool
	cmd := &cobra.Command{
		Use:     "mv <src> <dst>",
		Aliases: []string{"move", "rename"},
		Short:   "Move a secret or a tree: copy, verify, then wipe the sources",
		Long: `Move secrets. Two phases: first every destination is written and read back;
only if all of them check out are the sources wiped (metadata and versions).
By default the version history and metadata move along (KV v2 -> v2).`,
		Example: `  kvx mv secret/old-name secret/new-name
  kvx mv secret/legacy/ secret/archive/legacy/
  kvx mv secret/app/ secret/app/ --to-namespace team-b`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			src, err := a.resolve(ctx, args[0])
			if err != nil {
				return err
			}
			dc, err := a.remote(rf)
			if err != nil {
				return err
			}
			dst, err := dc.Resolve(ctx, args[1])
			if err != nil {
				return err
			}
			o.verify, o.allVersions, o.withMetadata = true, keepHistory, keepHistory
			cp, err := a.planCopy(ctx, src, dst, wf, o)
			if err != nil {
				return err
			}
			ops := cp.ops
			for _, p := range cp.moved {
				ops = append(ops, plan.Op{Kind: plan.Wipe, KV: src.KV, Path: p, Phase: 1, Label: "remove", Detail: "source, after copy is verified"})
			}
			return a.run(ctx, cmd, &m, ops, cp.skipped)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.onConflict, "on-conflict", "fail", "when dst exists and differs: fail, skip, overwrite or merge")
	f.BoolVar(&keepHistory, "history", true, "move version history and metadata too (--history=false: latest only)")
	rf.register(cmd, "move to")
	wf.register(cmd)
	m.register(cmd)
	return cmd
}

// ---------------------------------------------------------------- rm

func newRmCmd(a *app) *cobra.Command {
	var m mutationFlags
	var wf walkFlags
	var recursive, destroy, wipe, onlyEmpty, noRead bool
	var versionsFlag, olderThan string
	cmd := &cobra.Command{
		Use:     "rm <path>...",
		Aliases: []string{"delete"},
		Short:   "Delete secrets: soft delete (default), destroy versions, or wipe metadata",
		Long: `Three levels, from reversible to final:

  (default)   soft-delete the latest version (KV v2, reversible with undelete)
  --destroy   destroy version data, keep metadata
  --wipe      delete metadata and every version

On KV v1 every delete is final. Irreversible plans ask you to type "yes",
and kvx keeps the latest data of every secret in its backup (kvx undo).
A directory needs -r. --no-read --no-backup lists targets without reading
their state or saving a backup; use it when a secret cannot be read.`,
		Example: `  kvx rm secret/app/tmp
  kvx rm -r secret/sandbox/ --wipe --older-than 30d
  kvx rm secret/app/db --destroy --versions 1-3
  kvx rm -r secret/ --only-empty --dry-run`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			if destroy && wipe {
				return fmt.Errorf("--destroy and --wipe are exclusive")
			}
			if wipe && versionsFlag != "" {
				return fmt.Errorf("--wipe removes every version, --versions does not apply")
			}
			if noRead && !m.noBackup {
				return fmt.Errorf("--no-read requires --no-backup: secret data cannot be saved")
			}
			if noRead && (onlyEmpty || olderThan != "") {
				return fmt.Errorf("--no-read cannot be combined with --only-empty or --older-than")
			}
			if noRead && destroy && versionsFlag == "" {
				return fmt.Errorf("--no-read --destroy requires explicit --versions")
			}
			versions, err := vault.ParseVersions(versionsFlag)
			if err != nil {
				return err
			}
			var maxAge time.Duration
			if olderThan != "" {
				if maxAge, err = parseAge(olderThan); err != nil {
					return fmt.Errorf("--older-than: %w", err)
				}
			}

			var (
				mu      sync.Mutex
				ops     []plan.Op
				skipped []string
			)
			for _, arg := range args {
				ref, err := a.resolve(ctx, arg)
				if err != nil {
					return err
				}
				var paths []string
				var single bool
				if noRead {
					paths, single, err = a.deleteSources(ctx, ref, wf)
				} else {
					paths, single, err = a.sources(ctx, ref, wf)
				}
				if err != nil {
					return err
				}
				if !single && !recursive {
					return fmt.Errorf("%s is a directory (%d secrets): use -r", ref, len(paths))
				}
				if len(versions) > 0 && ref.KV.Version != 2 {
					return fmt.Errorf("%s: --versions needs KV v2", ref)
				}
				bar := a.progress.Start("Planning", len(paths))
				g, gctx := errgroup.WithContext(ctx)
				g.SetLimit(a.g.concurrency)
				for _, p := range paths {
					g.Go(func() error {
						defer bar.Add(1)
						if noRead {
							op := plan.Op{KV: ref.KV, Path: p, Versions: versions, Detail: "state not read; no backup"}
							switch {
							case wipe:
								op.Kind, op.Label = plan.Wipe, "wipe"
							case destroy:
								op.Kind, op.Label = plan.Destroy, "destroy"
							default:
								op.Kind, op.Label = plan.Delete, "delete"
							}
							mu.Lock()
							ops = append(ops, op)
							mu.Unlock()
							return nil
						}
						st, err := ref.KV.Stat(gctx, p)
						if err != nil {
							return err
						}
						full := ref.KV.Full(p)
						skip := func(why string) { mu.Lock(); skipped = append(skipped, full+" ("+why+")"); mu.Unlock() }
						switch {
						case !st.Exists:
							return nil
						case maxAge > 0 && st.Meta != nil && time.Since(st.Meta.UpdatedTime) < maxAge:
							return nil // filtered out, not worth listing
						case maxAge > 0 && st.Meta == nil:
							skip("--older-than needs KV v2")
							return nil
						case onlyEmpty && (st.Data == nil || !isEmpty(st.Data)):
							return nil
						case !destroy && !wipe && len(versions) == 0 && st.Data == nil:
							skip("already deleted")
							return nil
						}
						op := plan.Op{KV: ref.KV, Path: p, Versions: versions, Before: &st}
						switch {
						case wipe:
							op.Kind, op.Label, op.Detail = plan.Wipe, "wipe", "metadata and all versions"
							if st.Meta != nil {
								op.Detail = "metadata and " + output.Plural(st.Meta.Versions, "version")
							}
						case destroy:
							op.Kind, op.Label, op.Detail = plan.Destroy, "destroy", versionsLabel(versions, st)
						default:
							op.Kind, op.Label, op.Detail = plan.Delete, "delete", versionsLabel(versions, st)
							if ref.KV.Version == 1 {
								op.Detail = "KV v1: permanent"
							}
						}
						mu.Lock()
						ops = append(ops, op)
						mu.Unlock()
						return nil
					})
				}
				err = g.Wait()
				bar.Done()
				if err != nil {
					return err
				}
			}
			slices.Sort(skipped)
			return a.run(ctx, cmd, &m, ops, skipped)
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&recursive, "recursive", "r", false, "delete every secret below a directory")
	f.BoolVar(&destroy, "destroy", false, "destroy version data (irreversible in Vault)")
	f.BoolVar(&wipe, "wipe", false, "delete metadata and all versions (irreversible in Vault)")
	f.StringVar(&versionsFlag, "versions", "", "versions to delete or destroy, e.g. 1,2,5-7 (default: latest)")
	f.StringVar(&olderThan, "older-than", "", "only secrets not updated for this long (e.g. 90d)")
	f.BoolVar(&onlyEmpty, "only-empty", false, "only secrets with no keys or only empty values")
	f.BoolVar(&noRead, "no-read", false, "plan from listings without reading secret state (requires --no-backup)")
	wf.register(cmd)
	m.register(cmd)
	return cmd
}

func isEmpty(d map[string]any) bool {
	for _, v := range d {
		if strings.TrimSpace(output.Stringify(v)) != "" {
			return false
		}
	}
	return true
}

func versionsLabel(versions []int, st vault.State) string {
	if len(versions) > 0 {
		return fmt.Sprintf("versions %v", versions)
	}
	if st.Version > 0 {
		return fmt.Sprintf("latest (v%d)", st.Version)
	}
	return ""
}

// ---------------------------------------------------------------- undelete

func newUndeleteCmd(a *app) *cobra.Command {
	var m mutationFlags
	var wf walkFlags
	var recursive bool
	cmd := &cobra.Command{
		Use:   "undelete <path>[@version]...",
		Short: "Restore soft-deleted versions (KV v2)",
		Example: `  kvx undelete secret/app/db             # the latest version, if deleted
  kvx undelete secret/app/db@3
  kvx undelete -r secret/app/            # every secret whose latest version is deleted`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			var ops []plan.Op
			var skipped []string
			for _, arg := range args {
				ref, err := a.resolve(ctx, arg)
				if err != nil {
					return err
				}
				if ref.KV.Version != 2 {
					return fmt.Errorf("%s: undelete needs KV v2", ref)
				}
				paths, single, err := a.sources(ctx, ref, wf)
				if err != nil {
					return err
				}
				if !single && !recursive {
					return fmt.Errorf("%s is a directory: use -r", ref)
				}
				for _, p := range paths {
					meta, err := ref.KV.Metadata(ctx, p)
					if err != nil {
						return err
					}
					v := ref.Version
					if v == 0 {
						if !meta.CurrentDeleted {
							continue
						}
						v = meta.CurrentVersion
					}
					if meta.Destroyed(v) {
						skipped = append(skipped, fmt.Sprintf("%s@%d (destroyed, cannot be restored by Vault; try kvx undo)", ref.KV.Full(p), v))
						continue
					}
					ops = append(ops, plan.Op{Kind: plan.Undelete, KV: ref.KV, Path: p, Versions: []int{v}, Label: "undelete", Detail: fmt.Sprintf("v%d", v)})
				}
			}
			return a.run(ctx, cmd, &m, ops, skipped)
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "every deleted secret below a directory")
	wf.register(cmd)
	m.register(cmd)
	return cmd
}

// ---------------------------------------------------------------- rollback

func newRollbackCmd(a *app) *cobra.Command {
	var m mutationFlags
	cmd := &cobra.Command{
		Use:   "rollback <path>@<version>",
		Short: "Write an older version back as the new latest (KV v2)",
		Example: `  kvx diff secret/app/db@4        # check first
  kvx rollback secret/app/db@4`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			ref, err := a.resolve(ctx, args[0])
			if err != nil {
				return err
			}
			if ref.Version == 0 {
				return fmt.Errorf("give the version to restore: %s@N", ref)
			}
			old, err := ref.KV.Read(ctx, ref.Path, ref.Version)
			if err != nil {
				return err
			}
			cur, err := ref.KV.Stat(ctx, ref.Path)
			if err != nil {
				return err
			}
			if cur.Data != nil && plan.Equal(cur.Data, old) {
				return a.run(ctx, cmd, &m, nil, []string{fmt.Sprintf("%s (v%d already equals v%d)", ref.KV.Full(ref.Path), cur.Version, ref.Version)})
			}
			op := plan.Op{
				Kind: plan.Write, KV: ref.KV, Path: ref.Path, Data: old, CAS: cur.Version, Before: &cur,
				Label:  "rollback",
				Detail: fmt.Sprintf("v%d -> new v%d with the data of v%d%s", cur.Version, cur.Version+1, ref.Version, describeKeyChanges(cur.Data, old)),
			}
			return a.run(ctx, cmd, &m, []plan.Op{op}, nil)
		},
	}
	m.register(cmd)
	return cmd
}

// ---------------------------------------------------------------- undo

func newUndoCmd(a *app) *cobra.Command {
	var m mutationFlags
	var list bool
	cmd := &cobra.Command{
		Use:   "undo [snapshot-id]",
		Short: "Restore the secrets saved before the last (or a given) kvx change",
		Long: `Every kvx change first saves the secrets it is about to touch. undo puts
them back: data is rewritten as a new version, secrets that did not exist
are wiped, secrets that were soft-deleted are deleted again.

undo is itself a change, with its own snapshot: run undo twice to redo.
Snapshots hold plaintext values; they live in $KVX_BACKUP_DIR or
~/.local/state/kvx/backups, readable only by you.`,
		Example: `  kvx undo --list
  kvx undo                     # the latest snapshot
  kvx undo 20261125-1012 --dry-run`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			if list {
				all, err := snapshot.List()
				if err != nil {
					return err
				}
				type item struct {
					ID      string    `json:"id" yaml:"id"`
					Created time.Time `json:"created" yaml:"created"`
					Secrets int       `json:"secrets" yaml:"secrets"`
					Command string    `json:"command" yaml:"command"`
				}
				items := make([]item, len(all))
				rows := make([][]string, len(all))
				for i, s := range all {
					items[i] = item{s.ID, s.Created, len(s.Entries), s.Command}
					rows[i] = []string{s.ID, humanAge(s.Created) + " ago", fmt.Sprint(len(s.Entries)), s.Command}
				}
				if ok, err := a.out.Structured(items); ok {
					return err
				}
				return a.out.Table([]string{"ID", "AGE", "SECRETS", "COMMAND"}, rows)
			}
			var id string
			if len(args) == 1 {
				id = args[0]
			}
			s, err := snapshot.Load(id)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.errW, "Snapshot %s, %s ago: %s\n\n", s.ID, humanAge(s.Created), s.Command)
			ops, skipped, err := a.planUndo(ctx, s)
			if err != nil {
				return err
			}
			return a.run(ctx, cmd, &m, ops, skipped)
		},
	}
	cmd.Flags().BoolVar(&list, "list", false, "list snapshots, newest first")
	m.register(cmd)
	return cmd
}

func (a *app) planUndo(ctx context.Context, s *snapshot.Snapshot) ([]plan.Op, []string, error) {
	clients := map[string]*vault.Client{}
	var ops []plan.Op
	var skipped []string
	bar := a.progress.Start("Planning", len(s.Entries))
	defer bar.Done()
	for _, e := range s.Entries {
		key := e.Address + "|" + e.Namespace
		c, ok := clients[key]
		if !ok {
			var err error
			c, err = vault.NewClient(vault.Options{Address: e.Address, Token: a.g.token, Namespace: e.Namespace})
			if err != nil {
				return nil, nil, err
			}
			clients[key] = c
		}
		kv := c.KV(e.Mount, e.KVVersion)
		cur, err := kv.Stat(ctx, e.Path)
		bar.Add(1)
		if err != nil {
			return nil, nil, err
		}
		full := kv.Full(e.Path)
		op := plan.Op{KV: kv, Path: e.Path, Before: &cur, CAS: -1}
		switch e.State {
		case snapshot.Present:
			if cur.Data != nil && plan.Equal(cur.Data, e.Data) {
				skipped = append(skipped, full+" (already as saved)")
				continue
			}
			op.Kind, op.Data, op.Label = plan.Write, e.Data, "restore"
			if kv.Version == 2 {
				op.CAS = cur.Version
			}
			op.Detail = "saved data" + describeKeyChanges(cur.Data, e.Data)
			if !cur.Exists {
				op.Label = "create"
			}
		case snapshot.Absent:
			if !cur.Exists {
				skipped = append(skipped, full+" (already absent)")
				continue
			}
			op.Kind, op.Label, op.Detail = plan.Wipe, "wipe", "did not exist before"
		case snapshot.Deleted:
			if cur.Data == nil {
				skipped = append(skipped, full+" (already deleted)")
				continue
			}
			op.Kind, op.Label, op.Detail = plan.Delete, "delete", "was soft-deleted before"
		default:
			return nil, nil, fmt.Errorf("snapshot %s: unknown state %q for %s", s.ID, e.State, full)
		}
		ops = append(ops, op)
	}
	return ops, skipped, nil
}
