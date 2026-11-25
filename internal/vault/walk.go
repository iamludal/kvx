package vault

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/gobwas/glob"
	"golang.org/x/sync/errgroup"

	"github.com/iamludal/kvx/internal/progress"
)

// WalkOptions control a recursive listing.
type WalkOptions struct {
	// Concurrency is the number of LIST requests in flight. Default 16.
	Concurrency int
	// MaxDepth limits recursion (1 = direct children only). 0 = unlimited.
	MaxDepth int
	// Include/Exclude are glob patterns matched against secret paths relative
	// to the walk root ("**" crosses directories, "*" does not).
	Include []string
	Exclude []string
	// KeepDirs also returns the directories that MaxDepth stopped at, with a
	// trailing slash, so a depth-limited tree still shows them.
	KeepDirs bool
	// Progress, if set, shows a running count of the secrets found.
	Progress progress.Factory
}

// Walk returns every secret path under root (relative to the mount), sorted.
// If root is itself a secret and not a directory, it returns just root.
func (kv *KV) Walk(ctx context.Context, root string, o WalkOptions) ([]string, error) {
	root = strings.Trim(root, "/")
	if o.Concurrency <= 0 {
		o.Concurrency = 16
	}
	inc, err := compileGlobs(o.Include)
	if err != nil {
		return nil, err
	}
	exc, err := compileGlobs(o.Exclude)
	if err != nil {
		return nil, err
	}

	bar := o.Progress.Start("Listing "+kv.Full(root), 0)
	defer bar.Done()

	var (
		mu      sync.Mutex
		secrets []string
	)
	keep := func(full string) {
		rel := strings.TrimPrefix(strings.TrimPrefix(full, root), "/")
		if rel == "" {
			rel = path.Base(full)
		}
		if len(inc) > 0 && !matchAny(inc, rel) {
			return
		}
		if matchAny(exc, rel) {
			return
		}
		mu.Lock()
		secrets = append(secrets, full)
		mu.Unlock()
		bar.Add(1)
	}

	// Breadth-first, one level at a time: each level's LISTs run in parallel.
	frontier := []string{root}
	for depth := 1; len(frontier) > 0; depth++ {
		var next []string
		var nmu sync.Mutex
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(o.Concurrency)
		for _, dir := range frontier {
			g.Go(func() error {
				keys, err := kv.List(gctx, dir)
				if err != nil {
					return err
				}
				for _, k := range keys {
					child := strings.TrimSuffix(join(dir, k), "/")
					if strings.HasSuffix(k, "/") {
						if o.MaxDepth == 0 || depth < o.MaxDepth {
							nmu.Lock()
							next = append(next, child)
							nmu.Unlock()
						} else if o.KeepDirs {
							mu.Lock()
							secrets = append(secrets, child+"/")
							mu.Unlock()
						}
						continue
					}
					keep(child)
				}
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return nil, err
		}
		frontier = next
	}

	// Nothing listed: root may be a single secret.
	if len(secrets) == 0 && root != "" {
		if _, err := kv.Read(ctx, root, 0); err == nil {
			return []string{root}, nil
		}
	}
	sort.Strings(secrets)
	return secrets, nil
}

func join(dir, k string) string {
	if dir == "" {
		return k
	}
	return dir + "/" + k
}

func compileGlobs(patterns []string) ([]*glob.Pattern, error) {
	out := make([]*glob.Pattern, 0, len(patterns))
	for _, p := range patterns {
		g, err := glob.Compile(p, '/')
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

func matchAny(gs []*glob.Pattern, s string) bool {
	for _, g := range gs {
		if g.Match(s) {
			return true
		}
	}
	return false
}

// ReadAll reads many secrets in parallel and returns them keyed by path.
// Secrets whose latest version is deleted or destroyed are left out. prog, if
// set, shows a progress bar.
func (kv *KV) ReadAll(ctx context.Context, paths []string, concurrency int, prog progress.Factory) (map[string]map[string]any, error) {
	if concurrency <= 0 {
		concurrency = 16
	}
	bar := prog.Start("Reading", len(paths))
	defer bar.Done()
	out := make(map[string]map[string]any, len(paths))
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)
	for _, p := range paths {
		g.Go(func() error {
			d, err := kv.Read(gctx, p, 0)
			bar.Add(1)
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			mu.Lock()
			out[p] = d
			mu.Unlock()
			return nil
		})
	}
	return out, g.Wait()
}
