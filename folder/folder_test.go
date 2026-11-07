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

func TestFreeNumbersAnotherSourcesName(t *testing.T) {
	dir := t.TempDir()
	if got := Free(dir, "a", ".html", "https://x/1"); got != "a.html" {
		t.Fatalf("empty folder: %s", got)
	}
	if _, err := Put(dir, "a.html", []byte("1"), Meta{Source: "https://x/1"}); err != nil {
		t.Fatal(err)
	}
	if got := Free(dir, "a", ".html", "https://x/1"); got != "a.html" {
		t.Errorf("the same source keeps its name: %s", got)
	}
	if got := Free(dir, "a", ".html", "https://x/2"); got != "a-2.html" {
		t.Errorf("another source is numbered: %s", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Free(dir, "b", ".pdf", "https://x/3"); got != "b-2.pdf" {
		t.Errorf("a file with no sidecar is never replaced: %s", got)
	}
}
