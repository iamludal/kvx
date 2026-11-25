package vault

import (
	"context"
	"os"
	"slices"
	"testing"
)

// Runs against a real server seeded by scripts/dev-vault.sh:
//
//	eval "$(scripts/dev-vault.sh)" && KVX_INTEGRATION=1 go test ./...
func TestIntegrationWalk(t *testing.T) {
	if os.Getenv("KVX_INTEGRATION") == "" {
		t.Skip("set KVX_INTEGRATION=1 with a seeded dev Vault (scripts/dev-vault.sh)")
	}
	ctx := context.Background()
	c, err := NewClient(Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path    string
		version int
		opts    WalkOptions
		want    []string
	}{
		{"secret/", 2, WalkOptions{}, []string{"app/api", "app/cache", "app/db", "legacy/old", "prod/db", "staging/db"}},
		{"secret/app", 2, WalkOptions{Exclude: []string{"c*"}}, []string{"app/api", "app/db"}},
		{"secret/", 2, WalkOptions{Include: []string{"**/db"}}, []string{"app/db", "prod/db", "staging/db"}},
		{"secret/app/db", 2, WalkOptions{}, []string{"app/db"}},
		{"kv1/", 1, WalkOptions{}, []string{"team/a"}},
	} {
		ref, err := c.Resolve(ctx, tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if ref.KV.Version != tc.version {
			t.Errorf("%s: detected KV v%d, want v%d", tc.path, ref.KV.Version, tc.version)
		}
		got, err := ref.KV.Walk(ctx, ref.Path, tc.opts)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("Walk(%s, %+v) = %v, want %v", tc.path, tc.opts, got, tc.want)
		}
	}
}

func TestParseVersions(t *testing.T) {
	got, err := ParseVersions("1, 3-5,9")
	if err != nil || !slices.Equal(got, []int{1, 3, 4, 5, 9}) {
		t.Errorf("ParseVersions = %v, %v", got, err)
	}
	for _, bad := range []string{"0", "5-3", "a", "1-x"} {
		if _, err := ParseVersions(bad); err == nil {
			t.Errorf("ParseVersions(%q) should fail", bad)
		}
	}
}
