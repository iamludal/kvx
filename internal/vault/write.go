package vault

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/vault/api"
)

// Address is the Vault address this mount handle talks to.
func (kv *KV) Address() string { return kv.c.Address() }

// Namespace is the namespace this mount handle uses ("" for root).
func (kv *KV) Namespace() string { return kv.c.Namespace() }

// Put writes data as a new version (KV v2) or replaces the secret (KV v1).
// On KV v2, cas >= 0 enables check-and-set: 0 means "must not exist", N means
// "current version must be N". cas < 0 disables it.
func (kv *KV) Put(ctx context.Context, path string, data map[string]any, cas int) error {
	if kv.Version == 1 {
		if err := kv.c.KVv1(kv.Mount).Put(ctx, path, data); err != nil {
			return fmt.Errorf("writing %s: %w", kv.Full(path), err)
		}
		return nil
	}
	var opts []api.KVOption
	if cas >= 0 {
		opts = append(opts, api.WithCheckAndSet(cas))
	}
	if _, err := kv.c.KVv2(kv.Mount).Put(ctx, path, data, opts...); err != nil {
		if cas >= 0 && isCASError(err) {
			return fmt.Errorf("writing %s: changed since the plan was made (check-and-set %d failed); run again", kv.Full(path), cas)
		}
		return fmt.Errorf("writing %s: %w", kv.Full(path), err)
	}
	return nil
}

func isCASError(err error) bool {
	var re *api.ResponseError
	if !errors.As(err, &re) || re.StatusCode != 400 {
		return false
	}
	for _, e := range re.Errors {
		if containsAny(e, "check-and-set", "cas") {
			return true
		}
	}
	return false
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// PutMetadata writes KV v2 metadata settings. No-op on KV v1.
func (kv *KV) PutMetadata(ctx context.Context, path string, m *Meta) error {
	if kv.Version != 2 || m == nil {
		return nil
	}
	custom := make(map[string]any, len(m.CustomMetadata))
	for k, v := range m.CustomMetadata {
		custom[k] = v
	}
	in := api.KVMetadataPutInput{CustomMetadata: custom, MaxVersions: m.MaxVersions, CASRequired: m.CASRequired}
	if err := kv.c.KVv2(kv.Mount).PutMetadata(ctx, path, in); err != nil {
		return fmt.Errorf("writing metadata of %s: %w", kv.Full(path), err)
	}
	return nil
}

// SoftDelete marks versions as deleted (KV v2, reversible with Undelete).
// No versions means the latest. On KV v1 it deletes the secret for good.
func (kv *KV) SoftDelete(ctx context.Context, path string, versions []int) error {
	var err error
	switch {
	case kv.Version == 1:
		err = kv.c.KVv1(kv.Mount).Delete(ctx, path)
	case len(versions) == 0:
		err = kv.c.KVv2(kv.Mount).Delete(ctx, path)
	default:
		err = kv.c.KVv2(kv.Mount).DeleteVersions(ctx, path, versions)
	}
	if err != nil {
		return fmt.Errorf("deleting %s: %w", kv.Full(path), err)
	}
	return nil
}

// Destroy permanently removes the data of versions, keeping metadata (KV v2).
// No versions means the current version.
func (kv *KV) Destroy(ctx context.Context, path string, versions []int) error {
	if kv.Version == 1 {
		return kv.Wipe(ctx, path)
	}
	if len(versions) == 0 {
		m, err := kv.Metadata(ctx, path)
		if err != nil {
			return err
		}
		versions = []int{m.CurrentVersion}
	}
	if err := kv.c.KVv2(kv.Mount).Destroy(ctx, path, versions); err != nil {
		return fmt.Errorf("destroying %s: %w", kv.Full(path), err)
	}
	return nil
}

// Wipe deletes the metadata and every version (KV v2), or the secret (KV v1).
func (kv *KV) Wipe(ctx context.Context, path string) error {
	var err error
	if kv.Version == 1 {
		err = kv.c.KVv1(kv.Mount).Delete(ctx, path)
	} else {
		err = kv.c.KVv2(kv.Mount).DeleteMetadata(ctx, path)
	}
	if err != nil {
		return fmt.Errorf("wiping %s: %w", kv.Full(path), err)
	}
	return nil
}

// Undelete restores soft-deleted versions (KV v2).
func (kv *KV) Undelete(ctx context.Context, path string, versions []int) error {
	if kv.Version != 2 {
		return fmt.Errorf("%s: undelete needs KV v2", kv.Full(path))
	}
	if err := kv.c.KVv2(kv.Mount).Undelete(ctx, path, versions); err != nil {
		return fmt.Errorf("undeleting %s: %w", kv.Full(path), err)
	}
	return nil
}

// History returns the data of every readable version, oldest first (KV v2).
// Deleted and destroyed versions are skipped.
func (kv *KV) History(ctx context.Context, path string) ([]map[string]any, error) {
	if kv.Version != 2 {
		d, err := kv.Read(ctx, path, 0)
		if err != nil {
			return nil, err
		}
		return []map[string]any{d}, nil
	}
	vs, err := kv.c.KVv2(kv.Mount).GetVersionsAsList(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("listing versions of %s: %w", kv.Full(path), err)
	}
	var out []map[string]any
	for _, v := range vs {
		if v.Destroyed || !v.DeletionTime.IsZero() {
			continue
		}
		d, err := kv.Read(ctx, path, v.Version)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// State is what exists at a path right now.
type State struct {
	Exists  bool           // metadata or data exists
	Deleted bool           // exists but the latest version is deleted/destroyed
	Version int            // current version (KV v2), 0 if absent
	Data    map[string]any // latest data, nil if absent or deleted
	Meta    *Meta          // KV v2 metadata, nil otherwise
}

// Stat reads the current state of a secret without failing when it is absent.
func (kv *KV) Stat(ctx context.Context, path string) (State, error) {
	if kv.Version == 2 {
		m, err := kv.Metadata(ctx, path)
		if errors.Is(err, ErrNotFound) {
			return State{}, nil
		}
		if err != nil {
			return State{}, err
		}
		st := State{Exists: true, Version: m.CurrentVersion, Meta: m, Deleted: m.CurrentDeleted}
		if !m.CurrentDeleted {
			if st.Data, err = kv.Read(ctx, path, 0); err != nil && !errors.Is(err, ErrNotFound) {
				return State{}, err
			}
			st.Deleted = st.Data == nil
		}
		return st, nil
	}
	d, err := kv.Read(ctx, path, 0)
	if errors.Is(err, ErrNotFound) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	return State{Exists: true, Data: d}, nil
}

// ParseVersions parses "1,2,5-7" into [1 2 5 6 7].
func ParseVersions(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	var out []int
	for _, part := range strings.Split(s, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		a, err := strconv.Atoi(lo)
		if err != nil || a < 1 {
			return nil, fmt.Errorf("invalid version %q", part)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil || b < a {
				return nil, fmt.Errorf("invalid version range %q", part)
			}
		}
		for v := a; v <= b; v++ {
			out = append(out, v)
		}
	}
	return out, nil
}
