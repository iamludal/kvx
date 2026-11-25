package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadOrder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KVX_BACKUP_DIR", dir)

	var ids []string
	for i := range 5 { // same second: order must follow creation, not the random suffix
		s, _, err := Save("cmd", []Entry{{Mount: "secret", Path: "p", State: Present, Data: map[string]any{"i": i}}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID)
	}
	latest, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != ids[4] {
		t.Errorf("Load(\"\") = %s, want the last saved %s", latest.ID, ids[4])
	}
	if s, err := Load(ids[1]); err != nil || s.ID != ids[1] {
		t.Errorf("Load(%s) = %v, %v", ids[1], s, err)
	}
	if _, err := Load("nope"); err == nil {
		t.Error("Load(nope) should fail")
	}

	info, err := os.Stat(filepath.Join(dir, ids[0]+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("snapshot mode = %v, want 0600", info.Mode().Perm())
	}
}
