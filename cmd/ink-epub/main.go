// Command ink-epub packs each HTML item read from stdin into an EPUB in the
// library, and copies each PDF item there as it is.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/offware-apps/ink-drop/epub"
	"github.com/offware-apps/ink-drop/folder"
	"github.com/offware-apps/ink-drop/internal/stage"
)

func main() {
	to := flag.String("to", "", "library folder (required)")
	move := flag.Bool("move", false, "delete each item once its book is in the library, so an item left after a failed run is the only one waiting")
	width := flag.Int("max-width", 0, "shrink images wider than this many pixels, 0 for none")
	grey := flag.Bool("grey", false, "turn images grey, for a greyscale screen")
	flag.Parse()
	if *to == "" {
		fmt.Fprintln(os.Stderr, "ink-epub: -to names the library folder")
		os.Exit(2)
	}
	os.Exit(stage.Run("ink-epub", os.Stdin, os.Stdout, convert(*to, *move, look{*width, *grey})))
}

// convert returns what ink-epub does with one item: write its book into the
// library to, and with move delete the item once the book is there.
func convert(to string, move bool, l ...look) func(string) ([]string, error) {
	return func(line string) ([]string, error) {
		p, err := toLibrary(to, line, l...)
		// A book the library cannot take, a full disk for one, stops the run
		// with exit 2, so the caller keeps the page for later; exit 1 is
		// for a page that never will be a book.
		var w writeError
		if errors.As(err, &w) {
			fmt.Fprintln(os.Stderr, "ink-epub:", err)
			os.Exit(2)
		}
		// An item already in the library is its own book: it stays.
		if err == nil && move && !same(line, p) {
			folder.Remove(line)
		}
		return []string{p}, err
	}
}

// same reports whether a and b name one file.
func same(a, b string) bool {
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(sa, sb)
}

// look is how a book's images are packed: no wider than width, grey or not.
type look struct {
	width int
	grey  bool
}

// writeError is a failure to write into the library, never the page's fault.
type writeError struct{ error }

func (w writeError) Unwrap() error { return w.error }

// toLibrary writes the item at path into the library dir and returns the
// path of the book it wrote, named by folder.Free.
func toLibrary(dir, path string, l ...look) (string, error) {
	m, err := folder.Get(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	m.Stage = "ink-epub"
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	switch filepath.Ext(path) {
	case ".pdf":
		return put(dir, folder.Free(dir, stem, ".pdf", m.Source), data, m)
	case ".html":
		title := m.Title
		if title == "" {
			title = stem
		}
		var opt look
		if len(l) > 0 {
			opt = l[0]
		}
		book, err := epub.Build(epub.Book{Title: title, Source: m.Source, Modified: m.Fetched, MaxWidth: opt.width, Grey: opt.grey}, bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		return put(dir, folder.Free(dir, stage.Slug(title), ".epub", m.Source), book, m)
	}
	return "", errors.New("not an HTML or PDF item")
}

func put(dir, name string, data []byte, m folder.Meta) (string, error) {
	p, err := folder.Put(dir, name, data, m)
	if err != nil {
		return "", writeError{err}
	}
	return p, nil
}
