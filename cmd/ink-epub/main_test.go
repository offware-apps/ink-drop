package main

import (
	"path/filepath"
	"testing"

	"github.com/offware-apps/ink-drop/folder"
)

func TestToLibrary(t *testing.T) {
	work, lib := t.TempDir(), t.TempDir()
	html, err := folder.Put(work, "example-org-post.html", []byte("<html><body><p>texte</p></body></html>"), folder.Meta{Title: "Mon Article", Source: "https://example.org/post", Stage: "ink-readable"})
	if err != nil {
		t.Fatal(err)
	}
	pdf, err := folder.Put(work, "paper.pdf", []byte("%PDF-"), folder.Meta{Source: "https://example.org/paper.pdf", Stage: "ink-fetch"})
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{html: "mon-article.epub", pdf: "paper.pdf"} {
		out, err := toLibrary(lib, in)
		if err != nil {
			t.Fatal(err)
		}
		m, err := folder.Get(out)
		if filepath.Base(out) != want || err != nil || m.Stage != "ink-epub" || m.Source == "" {
			t.Errorf("%s: got %s, sidecar %+v, %v", in, out, m, err)
		}
	}
	if _, err := toLibrary(lib, filepath.Join(work, "missing.html")); err == nil {
		t.Error("an item without a sidecar was accepted")
	}
}
