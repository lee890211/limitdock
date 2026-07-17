package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFileCreatesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.json")

	if err := AtomicWriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "first" {
		t.Fatalf("read after create: %q err=%v", got, err)
	}

	if err := AtomicWriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, err = os.ReadFile(path)
	if err != nil || string(got) != "second" {
		t.Fatalf("read after overwrite: %q err=%v", got, err)
	}
}

func TestAtomicWriteFileLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.json")
	if err := AtomicWriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "target.json" {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("unexpected directory contents: %v", names)
	}
}

func TestAtomicWriteFileFailsWhenDirMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing", "target.json")
	if err := AtomicWriteFile(path, []byte("data"), 0o600); err == nil {
		t.Fatal("expected error for missing directory")
	}
}
