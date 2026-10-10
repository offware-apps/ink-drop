package folder

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPutGet(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	path, err := Put(dir, "article.epub", []byte("epub"), Meta{Source: "https://example.org/a", Fetched: at, Stage: "ink-epub"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Get(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.File != "article.epub" || m.Source != "https://example.org/a" || !m.Fetched.Equal(at) || m.Schema != Schema {
		t.Fatalf("got %+v", m)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("document mode %v, %v", fi.Mode(), err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("want the document and %s alone, got %d entries", SidecarDir, len(entries))
	}
}

func TestPutRejectsPaths(t *testing.T) {
	for _, name := range []string{"", "../x.epub", "a/b.epub", ".hidden"} {
		if _, err := Put(t.TempDir(), name, nil, Meta{}); err == nil {
			t.Errorf("Put accepted %q", name)
		}
	}
}

func TestGetRejectsForeignSidecar(t *testing.T) {
	dir := t.TempDir()
	path, err := Put(dir, "a.pdf", nil, Meta{})
	if err != nil {
		t.Fatal(err)
	}
	os.Rename(filepath.Join(dir, SidecarDir, "a.pdf.json"), filepath.Join(dir, SidecarDir, "b.pdf.json"))
	os.Rename(path, filepath.Join(dir, "b.pdf"))
	if _, err := Get(filepath.Join(dir, "b.pdf")); err == nil {
		t.Fatal("Get accepted a sidecar naming another file")
	}
}
