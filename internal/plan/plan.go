// Package plan runs every kvx change through the same steps: show the plan,
// confirm, snapshot what is about to change, then apply in parallel.
package plan

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"
	"golang.org/x/term"

	"github.com/iamludal/kvx/internal/output"
	"github.com/iamludal/kvx/internal/snapshot"
	"github.com/iamludal/kvx/internal/vault"
)

// Kind is what an operation does to a secret.
type Kind string

const (
	Write    Kind = "write"    // new version with Data (or every History entry)
	Delete   Kind = "delete"   // soft delete (KV v2), final delete on KV v1
	Destroy  Kind = "destroy"  // destroy version data (KV v2)
	Wipe     Kind = "wipe"     // delete metadata and all versions
	Undelete Kind = "undelete" // restore soft-deleted versions
)

// Op is one change to one secret.
type Op struct {
	Kind Kind
	KV   *vault.KV
	Path string

	Data     map[string]any   // Write: the new data
	History  []map[string]any // Write: replay these versions instead of Data
	Meta     *vault.Meta      // Write: also write these metadata settings
	CAS      int              // Write: check-and-set version, -1 to disable
	Versions []int            // Delete/Destroy/Undelete: versions (empty = latest)
	Verify   bool             // Write: read back and compare after writing

	// Phase orders ops: all ops of phase 0 must succeed before phase 1 starts
	// (mv copies and verifies, then deletes sources).
	Phase int
	// Label is the short action shown in the plan: create, update, merge...
	Label string
	// Detail is the human explanation shown next to the path.
	Detail string
	// Before is the current state, used for the snapshot. Nil = read it.
	Before *vault.State
}

// Full is the full path of the op's target.
func (o Op) Full() string { return o.KV.Full(o.Path) }

// Irreversible tells whether this op loses data that undelete cannot recover.
// (kvx snapshots still keep the latest data.)
func (o Op) Irreversible() bool {
	return o.Kind == Destroy || o.Kind == Wipe || (o.Kind == Delete && o.KV.Version == 1)
}

func (o Op) changesData() bool { return o.Kind != Undelete }

// Options control how a plan runs.
type Options struct {
	DryRun      bool
	Yes         bool
	NoBackup    bool
	Concurrency int
	Command     string // recorded in the snapshot
	In          io.Reader
	Err         io.Writer // prompts and progress
}

// Run shows, confirms, snapshots and applies ops.
func Run(ctx context.Context, out *output.Printer, ops []Op, skipped []string, o Options) error {
	if o.In == nil {
		o.In = os.Stdin
	}
	if o.Err == nil {
		o.Err = os.Stderr
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 16
	}
	sort.SliceStable(ops, func(i, j int) bool {
		if ops[i].Phase != ops[j].Phase {
			return ops[i].Phase < ops[j].Phase
		}
		return ops[i].Full() < ops[j].Full()
	})

	sort.Strings(skipped)
	if err := Print(out, ops, skipped); err != nil {
		return err
	}
	if len(ops) == 0 {
		fmt.Fprintln(o.Err, "Nothing to do.")
		return nil
	}
	if o.DryRun {
		fmt.Fprintln(o.Err, out.Dim("Dry run: nothing was changed."))
		return nil
	}
	if !o.Yes {
		if err := confirm(out, ops, o); err != nil {
			return err
		}
	}

	if !o.NoBackup {
		s, file, err := takeSnapshot(ctx, ops, o)
		if err != nil {
			return fmt.Errorf("backup failed, nothing was changed: %w", err)
		}
		if s != nil {
			fmt.Fprintf(o.Err, "%s %s %s\n", out.Dim("backup"), s.ID, out.Dim("("+file+")"))
		}
	}

	var done atomic.Int64
	for _, phase := range phases(ops) {
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(o.Concurrency)
		for _, op := range phase {
			g.Go(func() error {
				if err := apply(gctx, op); err != nil {
					return err
				}
				done.Add(1)
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			fmt.Fprintf(o.Err, "%s after %d of %d changes. Restore with `kvx undo`.\n", out.Red("Stopped"), done.Load(), len(ops))
			return err
		}
	}
	fmt.Fprintf(o.Err, "%s %s applied.\n", out.Green("✓"), output.Plural(int(done.Load()), "change"))
	return nil
}

type row struct {
	Action string `json:"action" yaml:"action"`
	Path   string `json:"path" yaml:"path"`
	Detail string `json:"detail,omitempty" yaml:"detail,omitempty"`
}

// Print writes the plan to stdout (a table, or JSON/YAML for review in CI).
func Print(out *output.Printer, ops []Op, skipped []string) error {
	rows := make([]row, len(ops))
	for i, op := range ops {
		rows[i] = row{Action: op.Label, Path: op.Full(), Detail: op.Detail}
	}
	if ok, err := out.Structured(map[string]any{"changes": rows, "skipped": skipped}); ok {
		return err
	}
	if len(rows) > 0 {
		table := make([][]string, len(rows))
		for i, r := range rows {
			table[i] = []string{paint(out, ops[i], r.Action), r.Path, out.Dim(r.Detail)}
		}
		if err := out.Table(nil, table); err != nil {
			return err
		}
	}
	for _, s := range skipped {
		fmt.Fprintln(out.W, out.Dim("  skip    "+s))
	}
	counts := map[string]int{}
	var order []string
	for _, r := range rows {
		if counts[r.Action] == 0 {
			order = append(order, r.Action)
		}
		counts[r.Action]++
	}
	parts := make([]string, 0, len(order)+1)
	for _, a := range order {
		parts = append(parts, fmt.Sprintf("%d %s", counts[a], a))
	}
	if len(skipped) > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", len(skipped)))
	}
	if len(parts) > 0 {
		fmt.Fprintf(out.W, "\nPlan: %s.\n", strings.Join(parts, ", "))
	}
	return nil
}

func paint(out *output.Printer, op Op, s string) string {
	switch {
	case op.Irreversible():
		return out.Red(out.Bold(s))
	case op.Kind == Delete:
		return out.Red(s)
	case op.Label == "create":
		return out.Green(s)
	}
	return out.Yellow(s)
}

func confirm(out *output.Printer, ops []Op, o Options) error {
	if f, ok := o.In.(*os.File); ok && !term.IsTerminal(int(f.Fd())) {
		return errors.New("refusing to apply without a terminal: review with --dry-run, then pass --yes")
	}
	irreversible := 0
	for _, op := range ops {
		if op.Irreversible() {
			irreversible++
		}
	}
	r := bufio.NewReader(o.In)
	if irreversible > 0 {
		fmt.Fprintf(o.Err, "\n%s %d of these changes cannot be undone in Vault (kvx keeps the latest data in its backup).\nType %s to apply: ",
			out.Red("Warning:"), irreversible, out.Bold("yes"))
		line, _ := r.ReadString('\n')
		if strings.TrimSpace(line) != "yes" {
			return errors.New("aborted, nothing was changed")
		}
		return nil
	}
	fmt.Fprintf(o.Err, "\nApply %s? [y/N] ", output.Plural(len(ops), "change"))
	line, _ := r.ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		return errors.New("aborted, nothing was changed")
	}
	return nil
}

func takeSnapshot(ctx context.Context, ops []Op, o Options) (*snapshot.Snapshot, string, error) {
	var (
		mu      sync.Mutex
		seen    = map[string]bool{}
		entries []snapshot.Entry
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(o.Concurrency)
	for _, op := range ops {
		if !op.changesData() {
			continue
		}
		e := snapshot.Entry{Address: op.KV.Address(), Namespace: op.KV.Namespace(), Mount: op.KV.Mount, KVVersion: op.KV.Version, Path: op.Path}
		mu.Lock()
		dup := seen[e.Key()]
		seen[e.Key()] = true
		mu.Unlock()
		if dup {
			continue
		}
		g.Go(func() error {
			st := op.Before
			if st == nil {
				s, err := op.KV.Stat(gctx, op.Path)
				if err != nil {
					return err
				}
				st = &s
			}
			switch {
			case !st.Exists:
				e.State = snapshot.Absent
			case st.Data == nil:
				e.State = snapshot.Deleted
			default:
				e.State, e.Data = snapshot.Present, st.Data
			}
			mu.Lock()
			entries = append(entries, e)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, "", err
	}
	if len(entries) == 0 {
		return nil, "", nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key() < entries[j].Key() })
	return snapshot.Save(o.Command, entries)
}

func phases(ops []Op) [][]Op {
	var out [][]Op
	for i := 0; i < len(ops); {
		j := i
		for j < len(ops) && ops[j].Phase == ops[i].Phase {
			j++
		}
		out = append(out, ops[i:j])
		i = j
	}
	return out
}

func apply(ctx context.Context, op Op) error {
	switch op.Kind {
	case Write:
		versions := op.History
		if len(versions) == 0 {
			versions = []map[string]any{op.Data}
		}
		cas := op.CAS
		for _, d := range versions {
			if err := op.KV.Put(ctx, op.Path, d, cas); err != nil {
				return err
			}
			if cas >= 0 {
				cas++
			}
		}
		if err := op.KV.PutMetadata(ctx, op.Path, op.Meta); err != nil {
			return err
		}
		if op.Verify {
			got, err := op.KV.Read(ctx, op.Path, 0)
			if err != nil {
				return fmt.Errorf("verifying %s: %w", op.Full(), err)
			}
			if !Equal(got, versions[len(versions)-1]) {
				return fmt.Errorf("verifying %s: data read back differs from what was written", op.Full())
			}
		}
		return nil
	case Delete:
		return op.KV.SoftDelete(ctx, op.Path, op.Versions)
	case Destroy:
		return op.KV.Destroy(ctx, op.Path, op.Versions)
	case Wipe:
		return op.KV.Wipe(ctx, op.Path)
	case Undelete:
		return op.KV.Undelete(ctx, op.Path, op.Versions)
	}
	return fmt.Errorf("unknown operation %q", op.Kind)
}

// Equal compares two secrets by value, treating numbers and strings the way
// Vault returns them (JSON round-trip).
func Equal(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok {
			return false
		}
		if output.Stringify(av) != output.Stringify(bv) && !reflect.DeepEqual(av, bv) {
			return false
		}
	}
	return true
}
