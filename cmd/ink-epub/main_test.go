package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
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

func TestPicturesFitted(t *testing.T) {
	work, lib := t.TempDir(), t.TempDir()
	wide := image.NewRGBA(image.Rect(0, 0, 1200, 100))
	for i := range wide.Pix {
		wide.Pix[i] = uint8(i * 7 % 251)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, wide); err != nil {
		t.Fatal(err)
	}
	page := `<html><body><img src="data:image/png;base64,` + base64.StdEncoding.EncodeToString(b.Bytes()) + `"></body></html>`
	in, err := folder.Put(work, "pic.html", []byte(page), folder.Meta{Title: "Pic", Source: "https://example.org/pic", Stage: "ink-readable"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := convert(lib, false, look{width: 300, grey: true})(in)
	if err != nil || len(out) != 1 {
		t.Fatalf("convert: %v, %v", out, err)
	}
	z, err := zip.OpenReader(out[0])
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	for _, f := range z.File {
		if !strings.HasSuffix(f.Name, ".png") {
			continue
		}
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(r)
		r.Close()
		if err != nil || img.Bounds().Dx() != 300 || img.ColorModel() != color.GrayModel {
			t.Fatalf("picture in the book: %v %v, %v", img.Bounds(), img.ColorModel(), err)
		}
		return
	}
	t.Fatal("the book holds no PNG picture")
}

func TestSameTitleTwice(t *testing.T) {
	work, lib := t.TempDir(), t.TempDir()
	var outs []string
	for _, name := range []string{"a.html", "b.html"} {
		in, err := folder.Put(work, name, []byte("<html><body><p>"+name+"</p></body></html>"), folder.Meta{Title: "Same", Source: "https://example.org/" + name, Stage: "ink-feed"})
		if err != nil {
			t.Fatal(err)
		}
		out, err := toLibrary(lib, in)
		if err != nil {
			t.Fatal(err)
		}
		outs = append(outs, filepath.Base(out))
	}
	if outs[0] != "same.epub" || outs[1] != "same-2.epub" {
		t.Errorf("want two books, got %v", outs)
	}
}

// A later run's book of the same title but another page keeps the earlier
// book, and the same page made again replaces its own.
func TestSameTitleAcrossRuns(t *testing.T) {
	work, lib := t.TempDir(), t.TempDir()
	build := func(name, source string) string {
		in, err := folder.Put(work, name, []byte("<html><body><p>"+name+"</p></body></html>"), folder.Meta{Title: "Weekly notes", Source: source, Stage: "ink-feed"})
		if err != nil {
			t.Fatal(err)
		}
		out, err := toLibrary(lib, in)
		if err != nil {
			t.Fatal(err)
		}
		return filepath.Base(out)
	}
	first := build("a.html", "https://example.org/issue-1")
	second := build("b.html", "https://example.org/issue-2")
	again := build("c.html", "https://example.org/issue-2")
	if first != "weekly-notes.epub" || second != "weekly-notes-2.epub" || again != "weekly-notes-2.epub" {
		t.Errorf("got %s, %s, %s", first, second, again)
	}
}

// A library that cannot take the book is the library's fault, never the
// page's: the error is a writeError, which stops the run with exit 2.
func TestWriteErrorIsTheLibrarys(t *testing.T) {
	work, lib := t.TempDir(), t.TempDir()
	in, err := folder.Put(work, "a.html", []byte("<html><body><p>a</p></body></html>"), folder.Meta{Title: "A", Stage: "ink-feed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lib, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(lib, 0o700) })
	_, err = toLibrary(lib, in)
	var w writeError
	if !errors.As(err, &w) {
		t.Fatalf("want a writeError, got %v", err)
	}
	if _, err := toLibrary(lib, filepath.Join(work, "missing.html")); errors.As(err, &w) {
		t.Fatalf("a missing page is the page's fault, got a writeError: %v", err)
	}
}

// With -move, an item leaves once its book is in the library, and a PDF of
// another page keeps its own book.
func TestMoveAndPDFNames(t *testing.T) {
	work, lib := t.TempDir(), t.TempDir()
	run := convert(lib, true)
	var books []string
	for _, src := range []string{"https://example.org/download.php?id=1", "https://example.org/download.php?id=2"} {
		// Both pages' items take the name doc.pdf, one after the other.
		in, err := folder.Put(work, "doc.pdf", []byte("%PDF-"+src), folder.Meta{Source: src, Stage: "ink-fetch"})
		if err != nil {
			t.Fatal(err)
		}
		out, err := run(in)
		if err != nil {
			t.Fatal(err)
		}
		books = append(books, out[0])
		if _, err := os.Stat(filepath.Join(work, "doc.pdf")); !os.IsNotExist(err) {
			t.Errorf("the item stayed after its book was made: %v", err)
		}
	}
	if books[0] == books[1] {
		t.Errorf("two pages' PDFs share one book: %v", books)
	}
}

// -move keeps an item whose book failed, and a library item that is its own book.
func TestMoveKeepsWhatIsNotTaken(t *testing.T) {
	work, lib := t.TempDir(), t.TempDir()
	stray := filepath.Join(work, "stray.html")
	os.WriteFile(stray, []byte("<p>no sidecar</p>"), 0o644)
	if _, err := convert(lib, true)(stray); err == nil {
		t.Fatal("an item with no sidecar made a book")
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("a failed item was deleted: %v", err)
	}
	pdf, err := folder.Put(lib, "paper.pdf", []byte("%PDF-x"), folder.Meta{Source: "https://example.org/paper.pdf", Stage: "ink-epub"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := convert(lib, true)(pdf); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pdf); err != nil {
		t.Errorf("a library item that is its own book was deleted: %v", err)
	}
}
