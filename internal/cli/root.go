// Package cli defines the kvx command tree.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamludal/kvx/internal/output"
	"github.com/iamludal/kvx/internal/progress"
	"github.com/iamludal/kvx/internal/vault"
)

// Build information, set by goreleaser through -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// ExitError carries a specific exit code (diff and audit exit 1 on findings,
// like `diff` and linters do, so kvx can gate CI pipelines).
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// globals holds the persistent flags shared by every command.
type globals struct {
	address     string
	token       string
	namespace   string
	kvVersion   int
	output      string
	showSecrets bool
	noColor     bool
	noProgress  bool
	concurrency int
	timeout     time.Duration
}

// app is what each command receives: a lazily built client and a printer.
type app struct {
	g      *globals
	client *vault.Client
	out    *output.Printer
	errW   io.Writer // prompts, progress, notes
	in     io.Reader // confirmation answers
	// progress starts progress bars on errW; nil when they are off.
	progress progress.Factory
}

func (a *app) init(cmd *cobra.Command) error {
	f, err := output.ParseFormat(a.g.output)
	if err != nil {
		return err
	}
	a.out = output.New(cmd.OutOrStdout(), f, a.g.showSecrets, a.g.noColor)
	a.errW, a.in = cmd.ErrOrStderr(), cmd.InOrStdin()
	a.progress = progress.For(a.errW, !a.g.noProgress)
	return nil
}

func (a *app) vault() (*vault.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	c, err := vault.NewClient(vault.Options{
		Address:   a.g.address,
		Token:     a.g.token,
		Namespace: a.g.namespace,
		KVVersion: a.g.kvVersion,
	})
	if err != nil {
		return nil, err
	}
	a.client = c
	return c, nil
}

func (a *app) resolve(ctx context.Context, p string) (vault.Ref, error) {
	c, err := a.vault()
	if err != nil {
		return vault.Ref{}, err
	}
	return c.Resolve(ctx, p)
}

func (a *app) ctx(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	if a.g.timeout > 0 {
		return context.WithTimeout(cmd.Context(), a.g.timeout)
	}
	return context.WithCancel(cmd.Context())
}

// walkFlags are shared by every command that recurses into a tree.
type walkFlags struct {
	depth   int
	include []string
	exclude []string
}

func (w *walkFlags) register(cmd *cobra.Command) {
	cmd.Flags().IntVarP(&w.depth, "depth", "d", 0, "maximum recursion depth (0 = unlimited)")
	cmd.Flags().StringArrayVar(&w.include, "include", nil, "only secrets whose relative path matches this glob (repeatable, ** crosses directories)")
	cmd.Flags().StringArrayVar(&w.exclude, "exclude", nil, "skip secrets whose relative path matches this glob (repeatable)")
}

func (w *walkFlags) options(a *app) vault.WalkOptions {
	return vault.WalkOptions{
		Concurrency: a.g.concurrency,
		MaxDepth:    w.depth,
		Include:     w.include,
		Exclude:     w.exclude,
		Progress:    a.progress,
	}
}

// NewRoot builds the kvx command tree.
func NewRoot() *cobra.Command {
	g := &globals{}
	a := &app{g: g}

	root := &cobra.Command{
		Use:   "kvx",
		Short: "Power tools for Vault and OpenBao KV secrets",
		Long: `kvx works on whole trees of KV secrets: browse, search, diff, audit,
copy, move and clean them up, on KV v1 and v2, Vault and OpenBao.

Paths always include the mount: secret/app/db. A trailing slash marks a
directory, @N selects a KV v2 version: secret/app/db@3.

Connection settings come from the usual VAULT_* environment variables
(VAULT_ADDR, VAULT_TOKEN, VAULT_NAMESPACE, VAULT_CACERT...) and the configured Vault token helper.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return a.init(cmd)
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&g.address, "address", "", "Vault address (default $VAULT_ADDR)")
	pf.StringVar(&g.token, "token", "", "Vault token (default $VAULT_TOKEN or Vault token helper); prefer the env var, flags end up in shell history")
	pf.StringVarP(&g.namespace, "namespace", "n", "", "Vault Enterprise / OpenBao namespace (default $VAULT_NAMESPACE)")
	pf.IntVar(&g.kvVersion, "kv-version", 0, "force KV engine version 1 or 2 (default: auto-detect)")
	pf.StringVarP(&g.output, "output", "o", "table", "output format: table, json or yaml")
	pf.BoolVarP(&g.showSecrets, "show-secrets", "S", false, "print secret values instead of masked fingerprints")
	pf.BoolVar(&g.noColor, "no-color", false, "disable colors (also honours $NO_COLOR)")
	pf.BoolVar(&g.noProgress, "no-progress", false, "hide progress bars (only shown on a terminal, for operations lasting more than a moment)")
	pf.IntVarP(&g.concurrency, "concurrency", "j", 16, "number of parallel requests to Vault")
	pf.DurationVar(&g.timeout, "timeout", 0, "abort the whole command after this duration (e.g. 30s, 5m)")

	root.AddGroup(
		&cobra.Group{ID: "read", Title: "Explore:"},
		&cobra.Group{ID: "write", Title: "Change (plan, confirm, backup):"},
		&cobra.Group{ID: "data", Title: "Move data in and out:"},
		&cobra.Group{ID: "misc", Title: "Other:"},
	)

	for _, c := range []*cobra.Command{
		newLsCmd(a), newTreeCmd(a), newGetCmd(a), newSearchCmd(a),
		newDiffCmd(a), newAuditCmd(a), newWhoamiCmd(a),
	} {
		c.GroupID = "read"
		root.AddCommand(c)
	}
	for _, c := range newWriteCmds(a) {
		root.AddCommand(c)
	}
	root.SetHelpCommandGroupID("misc")
	root.SetCompletionCommandGroupID("misc")
	return root
}

// Execute runs kvx with the process arguments and returns the exit code.
func Execute() int { return Run(os.Args[1:], os.Stdout, os.Stderr) }

// Run runs kvx with args and returns the exit code: 0 ok, 1 finding (see
// ExitError), 2 error (printed on stderr).
func Run(args []string, stdout, stderr io.Writer) int {
	root := NewRoot()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		var ee ExitError
		if errors.As(err, &ee) {
			return ee.Code
		}
		fmt.Fprintln(stderr, "kvx: "+strings.TrimSpace(err.Error()))
		return 2
	}
	return 0
}
