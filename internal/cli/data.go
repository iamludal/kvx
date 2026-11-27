package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"golang.org/x/term"

	"github.com/iamludal/kvx/internal/dump"
	"github.com/iamludal/kvx/internal/output"
	"github.com/iamludal/kvx/internal/plan"
	"github.com/iamludal/kvx/internal/vault"
)

// ---------------------------------------------------------------- export

type exportOptions struct {
	format       string
	file         string
	withMetadata bool
	armor        bool
	recipients   []string
}

func newExportCmd(a *app) *cobra.Command {
	var o exportOptions
	var wf walkFlags
	cmd := &cobra.Command{
		Use:   "export <path>",
		Short: "Dump a secret or a tree to JSON, YAML or .env, optionally encrypted with age",
		Long: `Write the latest data of a secret, or of every secret below a directory, to
stdout (or --file). Secrets whose latest version is deleted are left out.

json and yaml keep the tree structure and are what kvx import reads back;
--with-metadata adds custom_metadata, max_versions and cas_required (KV v2).
dotenv flattens everything into KEY=value lines: names are upper-cased like
kvx get --env, and in a tree they are prefixed with the secret's path
(db/main + password -> DB_MAIN_PASSWORD).

A plaintext dump is a liability: prefer --age-recipient, which encrypts the
output with age (https://age-encryption.org) for public keys (age1..., or SSH
ssh-ed25519/ssh-rsa keys) or a recipients file. --file creates the file
readable only by you.`,
		Example: `  kvx export secret/app/ > app.json
  kvx export secret/ --format yaml --age-recipient age1... > backup.yaml.age
  kvx export secret/ --age-recipient ~/.config/kvx/recipients.txt --armor --file backup.json.age
  kvx export secret/app/db --format dotenv --file .env
  kvx export secret/app/db@3 --with-metadata`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !slices.Contains([]string{dump.JSON, dump.YAML, dump.Dotenv}, o.format) {
				return fmt.Errorf("--format must be json, yaml or dotenv, not %q", o.format)
			}
			if o.format == dump.Dotenv && o.withMetadata {
				return errors.New("the dotenv format cannot hold metadata: use --format json or yaml with --with-metadata")
			}
			var recipients []age.Recipient
			if len(o.recipients) > 0 {
				var err error
				if recipients, err = dump.ParseRecipients(o.recipients); err != nil {
					return fmt.Errorf("--age-recipient: %w", err)
				}
			} else if o.armor {
				return errors.New("--armor only applies to encrypted output: add --age-recipient")
			}
			if o.file == "" && len(recipients) > 0 && !o.armor && isTerminal(cmd.OutOrStdout()) {
				return errors.New("refusing to print binary age output on a terminal: redirect it, use --file, or add --armor")
			}

			ctx, cancel := a.ctx(cmd)
			defer cancel()
			ref, err := a.resolve(ctx, args[0])
			if err != nil {
				return err
			}
			d, skipped, err := a.readDump(ctx, ref, wf, o.withMetadata)
			if err != nil {
				return err
			}
			if err := writeExport(cmd.OutOrStdout(), d, o, recipients); err != nil {
				return err
			}

			note := fmt.Sprintf("Exported %s from %s", output.Plural(len(d.Secrets), "secret"), d.Source)
			if len(recipients) > 0 {
				note += ", encrypted for " + output.Plural(len(recipients), "recipient")
			}
			if o.file != "" {
				note += " to " + o.file
			}
			if skipped > 0 {
				note += fmt.Sprintf(" (%d skipped: latest version deleted)", skipped)
			}
			fmt.Fprintln(a.errW, a.out.Dim(note+"."))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.format, "format", dump.JSON, "json, yaml or dotenv")
	f.StringVar(&o.file, "file", "", "write to this file (created with mode 0600) instead of stdout")
	f.BoolVar(&o.withMetadata, "with-metadata", false, "include custom_metadata, max_versions and cas_required (KV v2, json/yaml)")
	f.StringArrayVar(&o.recipients, "age-recipient", nil, "encrypt for this age or SSH public key, or the recipients in this file (repeatable)")
	f.BoolVarP(&o.armor, "armor", "a", false, "ASCII-armor the encrypted output (PEM-like text instead of binary)")
	wf.register(cmd)
	return cmd
}

// readDump reads what an export of ref holds. It returns how many secrets
// were left out because their latest version is deleted.
func (a *app) readDump(ctx context.Context, ref vault.Ref, wf walkFlags, withMeta bool) (*dump.Dump, int, error) {
	paths, single, err := a.sources(ctx, ref, wf)
	if err != nil {
		return nil, 0, err
	}
	if ref.Version > 0 && !single {
		return nil, 0, fmt.Errorf("%s: a version (@%d) only applies to a single secret", ref, ref.Version)
	}
	d := &dump.Dump{Source: ref.String(), Exported: time.Now(), Secrets: map[string]dump.Entry{}}
	if !single && !strings.HasSuffix(d.Source, "/") {
		d.Source += "/"
	}

	var (
		mu      sync.Mutex
		skipped int
	)
	bar := a.progress.Start("Reading", len(paths))
	defer bar.Done()
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(a.g.concurrency)
	for _, p := range paths {
		g.Go(func() error {
			defer bar.Add(1)
			data, err := ref.KV.Read(gctx, p, ref.Version)
			if errors.Is(err, vault.ErrNotFound) {
				if single && ref.Version > 0 {
					return err // the user asked for this exact version
				}
				mu.Lock()
				skipped++
				mu.Unlock()
				return nil
			}
			if err != nil {
				return err
			}
			e := dump.Entry{Data: data}
			if withMeta {
				m, err := ref.KV.Metadata(gctx, p)
				if err != nil {
					return err
				}
				e.Metadata = exportMeta(m)
			}
			rel := ""
			if !single {
				rel = relUnder(ref.Path, p)
			}
			mu.Lock()
			d.Secrets[rel] = e
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, 0, err
	}
	if len(d.Secrets) == 0 {
		return nil, 0, fmt.Errorf("nothing to export: the latest version of every secret at %s is deleted", ref)
	}
	return d, skipped, nil
}

// exportMeta keeps the settings worth restoring; nil when all are defaults.
func exportMeta(m *vault.Meta) *dump.Metadata {
	if m == nil || (len(m.CustomMetadata) == 0 && m.MaxVersions == 0 && !m.CASRequired) {
		return nil
	}
	out := &dump.Metadata{MaxVersions: m.MaxVersions, CASRequired: m.CASRequired}
	if len(m.CustomMetadata) > 0 {
		out.CustomMetadata = m.CustomMetadata
	}
	return out
}

func writeExport(stdout io.Writer, d *dump.Dump, o exportOptions, recipients []age.Recipient) (err error) {
	w := stdout
	if o.file != "" {
		f, err := os.OpenFile(o.file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		defer func() {
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				os.Remove(o.file) // never leave half a dump behind
			}
		}()
		if err := f.Chmod(0o600); err != nil { // the file may have existed with wider permissions
			return err
		}
		w = f
	}
	if len(recipients) == 0 {
		return dump.Encode(w, d, o.format)
	}
	ew, err := dump.Encrypt(w, recipients, o.armor)
	if err != nil {
		return err
	}
	if err := dump.Encode(ew, d, o.format); err != nil {
		return err
	}
	return ew.Close()
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// ---------------------------------------------------------------- import

type importOptions struct {
	onConflict string
	identity   string
	format     string
	include    []string
	exclude    []string
}

func newImportCmd(a *app) *cobra.Command {
	var m mutationFlags
	var o importOptions
	cmd := &cobra.Command{
		Use:   "import <path> [file]",
		Short: "Load a kvx export (or any JSON/YAML/.env) into a path; reads stdin without file",
		Long: `Write the secrets of a file (or stdin, or "-") below path, as a change:
plan, confirmation, backup (kvx undo), then apply with check-and-set.

The input can be a kvx export (json or yaml, with or without metadata), a
JSON/YAML map of relative paths to key/value objects, a single key/value
object, or a .env file. The last two hold one secret: give its full path, or
a directory/ when the input is a kvx export of a single secret (it keeps its
name). The format comes from --format, the file extension, or the content.

age-encrypted input (binary or armored) is decrypted with --age-identity:
an age identity file (age-keygen) or an unencrypted SSH private key.

Destinations that exist and differ stop the import unless you choose
--on-conflict; identical ones are skipped, so an import can be re-run.
Metadata from the input is written with the secrets that change.`,
		Example: `  kvx import secret/app-restored/ app.json
  kvx import secret/ backup.yaml.age --age-identity ~/.config/kvx/key.txt --dry-run
  kvx import secret/app/db .env --on-conflict merge
  age -d -i key.txt backup.yaml.age | kvx import secret/ --on-conflict skip --yes
  VAULT_ADDR=https://old:8200 kvx export secret/app/ | kvx import secret/app/ --include 'db/**'`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOnConflict(o.onConflict); err != nil {
				return err
			}
			format := o.format
			if format == "auto" {
				format = ""
			}
			if format != "" && !slices.Contains([]string{dump.JSON, dump.YAML, dump.Dotenv}, format) {
				return fmt.Errorf("--format must be auto, json, yaml or dotenv, not %q", o.format)
			}
			keep, err := vault.Filter(o.include, o.exclude)
			if err != nil {
				return err
			}

			name, fromStdin := "stdin", true
			var raw []byte
			if len(args) == 2 && args[1] != "-" {
				name, fromStdin = args[1], false
				if raw, err = os.ReadFile(name); err != nil {
					return err
				}
				if format == "" {
					format = dump.FormatFromName(name)
				}
			} else if raw, err = io.ReadAll(cmd.InOrStdin()); err != nil {
				return fmt.Errorf("reading stdin: %w", err)
			}
			if dump.IsEncrypted(raw) {
				if o.identity == "" {
					return fmt.Errorf("%s is age-encrypted: pass --age-identity <file>", name)
				}
				if raw, err = dump.Decrypt(raw, o.identity); err != nil {
					return err
				}
			}
			d, err := dump.Decode(raw, format)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}

			ctx, cancel := a.ctx(cmd)
			defer cancel()
			dst, err := a.resolve(ctx, args[0])
			if err != nil {
				return err
			}
			ops, skipped, err := a.planImport(ctx, d, dst, name, o.onConflict, keep)
			if err != nil {
				return err
			}
			// The data came through stdin: ask for confirmation on the terminal.
			if fromStdin && a.in == os.Stdin && !m.yes && !m.dryRun {
				if tty, err := os.Open("/dev/tty"); err == nil {
					defer tty.Close()
					a.in = tty
				}
			}
			return a.run(ctx, cmd, &m, ops, skipped)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.onConflict, "on-conflict", "fail", "when a destination exists and differs: fail, skip, overwrite or merge (keys, input wins)")
	f.StringVar(&o.identity, "age-identity", "", "decrypt age input with this identity file (age key or SSH private key)")
	f.StringVar(&o.format, "format", "auto", "input format: auto, json, yaml or dotenv")
	f.StringArrayVar(&o.include, "include", nil, "only secrets whose relative path matches this glob (repeatable, ** crosses directories)")
	f.StringArrayVar(&o.exclude, "exclude", nil, "skip secrets whose relative path matches this glob (repeatable)")
	m.register(cmd)
	return cmd
}

// importTarget is where the secret stored under rel lands below dst.
func importTarget(d *dump.Dump, dst vault.Ref, rel string) (string, error) {
	if rel != "" {
		return joinPath(dst.Path, rel), nil
	}
	if !dst.IsDir && dst.Path != "" {
		return dst.Path, nil
	}
	if d.Source == "" {
		return "", fmt.Errorf("%s is a directory, but the input holds a single secret: give its full path, e.g. %sname", dst, dst)
	}
	src := strings.TrimSuffix(d.Source, "/")
	if i := strings.LastIndex(src, "@"); i > strings.LastIndex(src, "/") {
		src = src[:i]
	}
	return joinPath(dst.Path, path.Base(src)), nil
}

func (a *app) planImport(ctx context.Context, d *dump.Dump, dst vault.Ref, from, mode string, keep func(string) bool) ([]plan.Op, []string, error) {
	if dst.Version > 0 {
		return nil, nil, fmt.Errorf("%s: cannot import into a version, drop @%d", dst, dst.Version)
	}
	type item struct {
		rel, to string
		e       dump.Entry
	}
	var items []item
	for _, rel := range d.Paths() {
		if rel != "" && !keep(rel) {
			continue
		}
		to, err := importTarget(d, dst, rel)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, item{rel, to, d.Secrets[rel]})
	}
	if len(items) == 0 {
		return nil, nil, errors.New("nothing to import: --include/--exclude left no secret")
	}

	var (
		mu        sync.Mutex
		ops       []plan.Op
		skipped   []string
		conflicts []string
	)
	bar := a.progress.Start("Planning", len(items))
	defer bar.Done()
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(a.g.concurrency)
	for _, it := range items {
		g.Go(func() error {
			defer bar.Add(1)
			cur, err := dst.KV.Stat(gctx, it.to)
			if err != nil {
				return err
			}
			full := dst.KV.Full(it.to)
			dec := decideWrite(mode, cur, it.e.Data)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case dec.conflict:
				conflicts = append(conflicts, full)
				return nil
			case dec.skip != "":
				skipped = append(skipped, full+" ("+dec.skip+")")
				return nil
			}
			op := plan.Op{Kind: plan.Write, KV: dst.KV, Path: it.to, CAS: -1, Before: &cur, Data: dec.data, Label: dec.label}
			if dst.KV.Version == 2 {
				op.CAS = cur.Version
			}
			detail := "from " + from
			if m := it.e.Metadata; m != nil && dst.KV.Version == 2 {
				op.Meta = &vault.Meta{CustomMetadata: m.CustomMetadata, MaxVersions: m.MaxVersions, CASRequired: m.CASRequired}
				detail += ", with metadata"
			}
			op.Detail = detail + dec.changes
			ops = append(ops, op)
			return nil
		})
	}
	err := g.Wait()
	bar.Done()
	if err != nil {
		return nil, nil, err
	}
	if err := conflictError(conflicts); err != nil {
		return nil, nil, err
	}
	return ops, skipped, nil
}
