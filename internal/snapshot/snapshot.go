// Package snapshot stores the state of secrets right before kvx changes them,
// so `kvx undo` can put them back.
//
// Snapshots are JSON files under $KVX_BACKUP_DIR, or
// $XDG_STATE_HOME/kvx/backups (default ~/.local/state/kvx/backups). They hold
// secret values in plaintext, so files are 0600 in a 0700 directory.
package snapshot

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// State values of an entry.
const (
	Present = "present" // secret existed: restore this data
	Absent  = "absent"  // secret did not exist: remove it
	Deleted = "deleted" // latest version was soft-deleted: soft-delete again
)

// Entry is the state of one secret before the change.
type Entry struct {
	Address   string         `json:"address"`
	Namespace string         `json:"namespace,omitempty"`
	Mount     string         `json:"mount"`
	KVVersion int            `json:"kv_version"`
	Path      string         `json:"path"`
	State     string         `json:"state"`
	Data      map[string]any `json:"data,omitempty"`
}

// Key identifies the secret an entry is about.
func (e Entry) Key() string {
	return e.Address + "|" + e.Namespace + "|" + e.Mount + "/" + e.Path
}

// Snapshot is one saved set of entries.
type Snapshot struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Command string    `json:"command"`
	Entries []Entry   `json:"entries"`
}

// Dir returns the snapshot directory.
func Dir() (string, error) {
	if d := os.Getenv("KVX_BACKUP_DIR"); d != "" {
		return d, nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "kvx", "backups"), nil
}

// Save writes a new snapshot and returns it.
func Save(command string, entries []Entry) (*Snapshot, string, error) {
	dir, err := Dir()
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("creating backup dir: %w", err)
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	now := time.Now()
	s := &Snapshot{
		ID:      now.Format("20060102-150405.000") + "-" + hex.EncodeToString(b),
		Created: now,
		Command: command,
		Entries: entries,
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, "", err
	}
	file := filepath.Join(dir, s.ID+".json")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return nil, "", fmt.Errorf("writing backup: %w", err)
	}
	return s, file, nil
}

// List returns all snapshots, newest first.
func List() ([]*Snapshot, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []*Snapshot
	for _, f := range files {
		s, err := read(f)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// Load returns the snapshot with this id (a unique prefix is enough), or the
// latest one when id is empty.
func Load(id string) (*Snapshot, error) {
	all, err := List()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, errors.New("no snapshots yet: kvx saves one before every change")
	}
	if id == "" {
		return all[0], nil
	}
	var found []*Snapshot
	for _, s := range all {
		if strings.HasPrefix(s.ID, id) {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no snapshot %q (see `kvx undo --list`)", id)
	case 1:
		return found[0], nil
	}
	return nil, fmt.Errorf("snapshot id %q is ambiguous (%d matches)", id, len(found))
}

func read(file string) (*Snapshot, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("reading %s: %w", file, err)
	}
	return &s, nil
}
