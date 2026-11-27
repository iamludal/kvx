package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Commands in this file are designed (usage, flags, examples) but not yet
// implemented. Those that change data will go through plan.Run like the
// commands in mutate.go.

func notImplemented(cmd *cobra.Command, _ []string) error {
	return fmt.Errorf("`kvx %s` is not implemented yet", cmd.Name())
}

func newWriteCmds(a *app) []*cobra.Command {
	cmds := []*cobra.Command{newCpCmd(a), newMvCmd(a), newRmCmd(a), newUndeleteCmd(a), newRollbackCmd(a), newUndoCmd(a)}
	for _, c := range cmds {
		c.GroupID = "write"
	}
	mk := func(group string, c *cobra.Command, mutates bool) *cobra.Command {
		c.GroupID = group
		if c.RunE == nil {
			c.RunE = notImplemented
		}
		if mutates {
			(&mutationFlags{}).register(c)
		}
		cmds = append(cmds, c)
		return c
	}
	var wf walkFlags

	// ---- change (not implemented yet)
	rep := mk("write", &cobra.Command{
		Use:     "replace <path> <search> <replacement>",
		Aliases: []string{"sed"},
		Short:   "Search and replace in values (or key names) across a tree",
		Example: `  kvx replace secret/ db-old.internal db-new.internal --values
  kvx replace secret/ -E 'postgres://([^@]+)@old' 'postgres://$1@new'
  kvx replace secret/ DB_PASS DB_PASSWORD --keys     # rename a key everywhere`,
		Args: cobra.ExactArgs(3),
	}, true)
	rep.Flags().BoolP("regex", "E", false, "search is a regular expression ($1 in replacement)")
	rep.Flags().BoolP("keys", "k", false, "replace in key names")
	rep.Flags().BoolP("values", "v", false, "replace in values (default)")
	rep.Flags().BoolP("ignore-case", "i", false, "case-insensitive")
	wf.register(rep)

	norm := mk("write", &cobra.Command{
		Use:   "normalize <path>",
		Short: "Rewrite key names to a naming convention, with a collision check",
		Example: `  kvx normalize secret/app/ --case upper-snake     # dbPassword -> DB_PASSWORD
  kvx normalize secret/ --case lower-kebab --dry-run`,
		Args: cobra.ExactArgs(1),
	}, true)
	norm.Flags().String("case", "upper-snake", "upper-snake, lower-snake, camel, lower-kebab")
	norm.Flags().Bool("trim", true, "also trim leading/trailing whitespace in values")
	wf.register(norm)

	mk("write", &cobra.Command{
		Use:   "edit <path>",
		Short: "Edit a secret in $EDITOR as YAML, with check-and-set against concurrent writes",
		Args:  cobra.ExactArgs(1),
	}, false).Flags().String("format", "yaml", "yaml, json or dotenv")

	// ---- data
	for _, c := range []*cobra.Command{newExportCmd(a), newImportCmd(a)} {
		c.GroupID = "data"
		cmds = append(cmds, c)
	}

	ex := mk("data", &cobra.Command{
		Use:   "exec <path>... -- <command> [args...]",
		Short: "Run a command with secrets injected as environment variables",
		Example: `  kvx exec secret/app/db secret/app/api -- ./server
  kvx exec secret/app/db --prefix DB_ -- psql`,
		Args: cobra.MinimumNArgs(2),
	}, false)
	ex.Flags().String("prefix", "", "prefix every variable name")
	ex.Flags().Bool("no-uppercase", false, "keep key names as they are")

	return cmds
}
