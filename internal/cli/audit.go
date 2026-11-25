package cli

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/iamludal/kvx/internal/output"
	"github.com/iamludal/kvx/internal/vault"
)

var allChecks = []string{"stale", "empty", "duplicate", "deleted", "versions", "naming"}

type finding struct {
	Check  string `json:"check" yaml:"check"`
	Path   string `json:"path" yaml:"path"`
	Key    string `json:"key,omitempty" yaml:"key,omitempty"`
	Detail string `json:"detail" yaml:"detail"`
}

func newAuditCmd(a *app) *cobra.Command {
	var checks []string
	var olderThan, keyPattern string
	var maxVersions, minDupLen int
	var exitCode bool
	var wf walkFlags
	cmd := &cobra.Command{
		Use:   "audit <path>",
		Short: "Report stale, empty, duplicated, deleted or badly named secrets",
		Long: `Run hygiene checks over a tree of secrets:

  stale      not updated for longer than --older-than (KV v2)
  empty      secret with no keys, or a key with an empty value
  duplicate  the same value stored in several places (rotation risk)
  deleted    latest version soft-deleted or destroyed, metadata left behind (KV v2)
  versions   more than --max-versions versions kept (KV v2)
  naming     key names not matching --key-pattern (only with --key-pattern)

Values are never printed: duplicates are found by hash.`,
		Example: `  kvx audit secret/
  kvx audit secret/ --check stale --older-than 90d
  kvx audit secret/ --key-pattern '^[A-Z][A-Z0-9_]*$'
  kvx audit secret/ -o json --exit-code      # fail CI on findings`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			for _, c := range checks {
				if !slices.Contains(allChecks, c) {
					return fmt.Errorf("unknown check %q (available: %s)", c, strings.Join(allChecks, ", "))
				}
			}
			on := func(c string) bool { return len(checks) == 0 || slices.Contains(checks, c) }
			maxAge, err := parseAge(olderThan)
			if err != nil {
				return fmt.Errorf("--older-than: %w", err)
			}
			var keyRe *regexp.Regexp
			if keyPattern != "" {
				if keyRe, err = regexp.Compile(keyPattern); err != nil {
					return fmt.Errorf("--key-pattern: %w", err)
				}
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
				mu       sync.Mutex
				findings []finding
				hashes   = map[string][]string{} // value hash -> "path#key"
			)
			add := func(f finding) { mu.Lock(); findings = append(findings, f); mu.Unlock() }
			needMeta := ref.KV.Version == 2 && (on("stale") || on("deleted") || on("versions"))

			g, gctx := errgroup.WithContext(ctx)
			g.SetLimit(a.g.concurrency)
			for _, p := range paths {
				g.Go(func() error {
					full := ref.KV.Full(p)
					var meta *vault.Meta
					if needMeta {
						m, err := ref.KV.Metadata(gctx, p)
						if err != nil {
							return err
						}
						meta = m
						if m.CurrentDeleted {
							if on("deleted") {
								add(finding{Check: "deleted", Path: full, Detail: fmt.Sprintf("v%d is deleted; `kvx undelete` or `kvx rm --wipe`", m.CurrentVersion)})
							}
							return nil
						}
						if on("stale") && time.Since(m.UpdatedTime) > maxAge {
							add(finding{Check: "stale", Path: full, Detail: "last updated " + humanAge(m.UpdatedTime) + " ago (" + m.UpdatedTime.Format("2006-01-02") + ")"})
						}
						if on("versions") && m.Versions > maxVersions {
							add(finding{Check: "versions", Path: full, Detail: fmt.Sprintf("%d versions kept (max_versions=%d)", m.Versions, m.MaxVersions)})
						}
					}
					if !on("empty") && !on("duplicate") && !on("naming") {
						return nil
					}
					d, err := ref.KV.Read(gctx, p, 0)
					if errors.Is(err, vault.ErrNotFound) {
						if on("deleted") && meta == nil {
							add(finding{Check: "deleted", Path: full, Detail: "listed but unreadable"})
						}
						return nil
					}
					if err != nil {
						return err
					}
					if on("empty") && len(d) == 0 {
						add(finding{Check: "empty", Path: full, Detail: "secret has no keys"})
					}
					for k, v := range d {
						s := output.Stringify(v)
						if on("empty") && strings.TrimSpace(s) == "" {
							add(finding{Check: "empty", Path: full, Key: k, Detail: "empty value"})
						}
						if on("naming") && keyRe != nil && !keyRe.MatchString(k) {
							add(finding{Check: "naming", Path: full, Key: k, Detail: "key does not match " + keyPattern})
						}
						if on("duplicate") && len(s) >= minDupLen {
							h := output.Fingerprint(s)
							mu.Lock()
							hashes[h] = append(hashes[h], full+"#"+k)
							mu.Unlock()
						}
					}
					return nil
				})
			}
			if err := g.Wait(); err != nil {
				return err
			}
			for h, locs := range hashes {
				if len(locs) < 2 {
					continue
				}
				sort.Strings(locs)
				for _, l := range locs {
					path, key, _ := strings.Cut(l, "#")
					add(finding{Check: "duplicate", Path: path, Key: key, Detail: fmt.Sprintf("same value (***%s) in %d places", h[:6], len(locs))})
				}
			}
			sort.Slice(findings, func(i, j int) bool {
				if findings[i].Check != findings[j].Check {
					return slices.Index(allChecks, findings[i].Check) < slices.Index(allChecks, findings[j].Check)
				}
				if findings[i].Path != findings[j].Path {
					return findings[i].Path < findings[j].Path
				}
				return findings[i].Key < findings[j].Key
			})

			if ok, err := a.out.Structured(findings); ok {
				if err != nil {
					return err
				}
				return exitIf(exitCode && len(findings) > 0, 1)
			}
			if len(findings) == 0 {
				fmt.Fprintf(a.out.W, "%s %s checked, no findings\n", a.out.Green("✓"), output.Plural(len(paths), "secret"))
				return nil
			}
			rows := make([][]string, 0, len(findings))
			for _, f := range findings {
				rows = append(rows, []string{a.out.Yellow(f.Check), f.Path, orDash(f.Key), f.Detail})
			}
			if err := a.out.Table([]string{"CHECK", "PATH", "KEY", "DETAIL"}, rows); err != nil {
				return err
			}
			fmt.Fprintf(a.out.W, "\n%s in %s\n", output.Plural(len(findings), "finding"), output.Plural(len(paths), "secret"))
			return exitIf(exitCode, 1)
		},
	}
	f := cmd.Flags()
	f.StringSliceVarP(&checks, "check", "c", nil, "checks to run, comma-separated (default: all): "+strings.Join(allChecks, ","))
	f.StringVar(&olderThan, "older-than", "180d", "stale threshold (e.g. 90d, 12w, 720h)")
	f.IntVar(&maxVersions, "max-versions", 20, "versions check threshold")
	f.IntVar(&minDupLen, "min-duplicate-length", 8, "ignore values shorter than this for duplicate detection (avoids true/false/ports)")
	f.StringVar(&keyPattern, "key-pattern", "", "regex every key name must match (enables the naming check)")
	f.BoolVar(&exitCode, "exit-code", false, "exit 1 when there are findings (for CI)")
	wf.register(cmd)
	return cmd
}
