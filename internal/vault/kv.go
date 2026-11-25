package vault

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/vault/api"
)

// KV is a handle on one KV mount (v1 or v2). Paths given to its methods are
// relative to the mount and never start or end with a slash.
type KV struct {
	c       *api.Client
	Mount   string // mount path without trailing slash, e.g. "secret"
	Version int    // 1 or 2
}

// Ref is a user-supplied location split into its mount and relative path.
// "secret/app/db@3" -> Mount "secret", Path "app/db", Version 3.
type Ref struct {
	KV      *KV
	Path    string
	Version int  // requested secret version (KV v2 only), 0 = latest
	IsDir   bool // the user wrote a trailing slash
}

// String renders the ref as a full path.
func (r Ref) String() string {
	if r.Version > 0 {
		return r.KV.Full(r.Path) + "@" + strconv.Itoa(r.Version)
	}
	return r.KV.Full(r.Path)
}

// Full joins the mount and a relative path.
func (kv *KV) Full(rel string) string {
	if rel == "" {
		return kv.Mount + "/"
	}
	return kv.Mount + "/" + rel
}

// KV returns a handle on a mount whose KV version is already known.
func (c *Client) KV(mount string, version int) *KV {
	return &KV{c: c.API, Mount: strings.Trim(mount, "/"), Version: version}
}

// Resolve splits a full path into its mount and relative path, detecting the
// KV version through sys/internal/ui/mounts, which works with tokens that
// cannot read sys/mounts.
func (c *Client) Resolve(ctx context.Context, full string) (Ref, error) {
	full = strings.TrimPrefix(strings.TrimSpace(full), "/")
	if full == "" {
		return Ref{}, errors.New("empty path: expected <mount>/<path>, e.g. secret/app")
	}
	ref := Ref{IsDir: strings.HasSuffix(full, "/")}
	if i := strings.LastIndex(full, "@"); i > 0 && !strings.Contains(full[i:], "/") {
		v, err := strconv.Atoi(full[i+1:])
		if err != nil || v < 1 {
			return Ref{}, fmt.Errorf("invalid version in %q: expected path@N with N >= 1", full)
		}
		ref.Version = v
		full = full[:i]
	}

	mount, version, err := c.detectMount(ctx, full)
	if err != nil {
		return Ref{}, err
	}
	if c.opts.KVVersion != 0 {
		version = c.opts.KVVersion
	}
	ref.KV = &KV{c: c.API, Mount: mount, Version: version}
	ref.Path = strings.Trim(strings.TrimPrefix(full, mount), "/")
	if ref.Version != 0 && version != 2 {
		return Ref{}, fmt.Errorf("%s: versions (@%d) need a KV v2 mount, %s/ is KV v1", full, ref.Version, mount)
	}
	return ref, nil
}

func (c *Client) detectMount(ctx context.Context, full string) (string, int, error) {
	s, err := c.API.Logical().ReadWithContext(ctx, "sys/internal/ui/mounts/"+full)
	if err != nil || s == nil || s.Data == nil {
		// Fallback: first segment is the mount, KV version from the flag or 2.
		mount := strings.SplitN(full, "/", 2)[0]
		v := c.opts.KVVersion
		if v == 0 {
			v = 2
		}
		if err != nil && isForbidden(err) {
			return mount, v, nil
		}
		if err != nil {
			return "", 0, fmt.Errorf("detecting mount for %q: %w", full, err)
		}
		return mount, v, nil
	}
	mount := strings.TrimSuffix(fmt.Sprint(s.Data["path"]), "/")
	if t := fmt.Sprint(s.Data["type"]); t != "kv" && t != "generic" {
		return "", 0, fmt.Errorf("%s/ is a %q engine, kvx only works on KV mounts", mount, t)
	}
	version := 1
	if opts, ok := s.Data["options"].(map[string]any); ok && fmt.Sprint(opts["version"]) == "2" {
		version = 2
	}
	return mount, version, nil
}

func isForbidden(err error) bool {
	var re *api.ResponseError
	return errors.As(err, &re) && re.StatusCode == http.StatusForbidden
}

// List returns the entries directly under dir. Sub-directories end with "/".
// A missing path returns an empty list, not an error.
func (kv *KV) List(ctx context.Context, dir string) ([]string, error) {
	p := kv.Mount + "/" + dir
	if kv.Version == 2 {
		p = kv.Mount + "/metadata/" + dir
	}
	s, err := kv.c.Logical().ListWithContext(ctx, strings.TrimSuffix(p, "/"))
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", kv.Full(dir), err)
	}
	if s == nil || s.Data == nil {
		return nil, nil
	}
	raw, _ := s.Data["keys"].([]any)
	keys := make([]string, 0, len(raw))
	for _, k := range raw {
		keys = append(keys, fmt.Sprint(k))
	}
	sort.Strings(keys)
	return keys, nil
}

// ErrNotFound is returned when a secret does not exist (or its version is
// deleted or destroyed).
var ErrNotFound = errors.New("secret not found")

// Read returns the key/value data of a secret. version 0 means latest.
func (kv *KV) Read(ctx context.Context, path string, version int) (map[string]any, error) {
	if kv.Version == 1 {
		s, err := kv.c.Logical().ReadWithContext(ctx, kv.Mount+"/"+path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", kv.Full(path), err)
		}
		if s == nil || s.Data == nil {
			return nil, fmt.Errorf("%s: %w", kv.Full(path), ErrNotFound)
		}
		return s.Data, nil
	}
	var data map[string][]string
	if version > 0 {
		data = map[string][]string{"version": {strconv.Itoa(version)}}
	}
	s, err := kv.c.Logical().ReadWithDataWithContext(ctx, kv.Mount+"/data/"+path, data)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", kv.Full(path), err)
	}
	if s == nil || s.Data == nil {
		return nil, fmt.Errorf("%s: %w", kv.Full(path), ErrNotFound)
	}
	d, _ := s.Data["data"].(map[string]any)
	if d == nil {
		// Soft-deleted or destroyed version: data is null, metadata is set.
		return nil, fmt.Errorf("%s: %w (deleted or destroyed version)", kv.Full(path), ErrNotFound)
	}
	return d, nil
}

// Meta is the KV v2 metadata kvx cares about.
type Meta struct {
	CreatedTime    time.Time
	UpdatedTime    time.Time
	CurrentVersion int
	OldestVersion  int
	Versions       int
	MaxVersions    int
	CASRequired    bool
	// CurrentDeleted is true when the latest version is soft-deleted or destroyed.
	CurrentDeleted bool
	CustomMetadata map[string]string

	destroyed map[int]bool
}

// Destroyed tells whether version v was destroyed.
func (m *Meta) Destroyed(v int) bool { return m.destroyed[v] }

// Metadata reads the metadata of a KV v2 secret. On KV v1 it returns nil, nil.
func (kv *KV) Metadata(ctx context.Context, path string) (*Meta, error) {
	if kv.Version != 2 {
		return nil, nil
	}
	m, err := kv.c.KVv2(kv.Mount).GetMetadata(ctx, path)
	if err != nil {
		if errors.Is(err, api.ErrSecretNotFound) {
			return nil, fmt.Errorf("%s: %w", kv.Full(path), ErrNotFound)
		}
		return nil, fmt.Errorf("reading metadata of %s: %w", kv.Full(path), err)
	}
	out := &Meta{
		CreatedTime:    m.CreatedTime,
		UpdatedTime:    m.UpdatedTime,
		CurrentVersion: m.CurrentVersion,
		OldestVersion:  m.OldestVersion,
		Versions:       len(m.Versions),
		MaxVersions:    m.MaxVersions,
		CASRequired:    m.CASRequired,
		CustomMetadata: map[string]string{},
	}
	for k, v := range m.CustomMetadata {
		out.CustomMetadata[k] = fmt.Sprint(v)
	}
	out.destroyed = map[int]bool{}
	for k, v := range m.Versions {
		if n, err := strconv.Atoi(k); err == nil && v.Destroyed {
			out.destroyed[n] = true
		}
	}
	if cur, ok := m.Versions[strconv.Itoa(m.CurrentVersion)]; ok {
		out.CurrentDeleted = cur.Destroyed || !cur.DeletionTime.IsZero()
	}
	return out, nil
}
