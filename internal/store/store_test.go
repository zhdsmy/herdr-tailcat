package store

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAtomicReplacementAndPermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "private", "state.json")
	if err := WriteJSON(p, map[string]string{"value": "old"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			var v map[string]string
			if err := ReadJSON(p, &v); err != nil {
				t.Error(err)
			}
			if v["value"] != "old" && v["value"] != "new" {
				t.Error("partial JSON observed")
			}
		}
	}()
	for range 30 {
		if err := WriteJSON(p, map[string]string{"value": "new"}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	for path, want := range map[string]os.FileMode{p: 0600, filepath.Dir(p): 0700} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != want {
			t.Fatalf("permissions %s: %v", path, err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".write-*"))
	if len(files) != 0 {
		t.Fatal("temporary files leaked")
	}
	if err := WriteJSON(p, make(chan int)); err == nil {
		t.Fatal("unencodable data accepted")
	}
	var v map[string]string
	if err := ReadJSON(p, &v); err != nil || v["value"] != "new" {
		t.Fatal("failed write replaced existing value")
	}
}
func TestCorruptedFileErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "broken.json")
	os.WriteFile(p, []byte("{broken"), 0600)
	var v Profile
	if ReadJSON(p, &v) == nil {
		t.Fatal("corruption accepted")
	}
}
