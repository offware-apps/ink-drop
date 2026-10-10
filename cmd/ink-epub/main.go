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
	flag.Parse()
	if *to == "" {
		fmt.Fprintln(os.Stderr, "ink-epub: -to names the library folder")
		os.Exit(2)
	}
	os.Exit(stage.Run("ink-epub", os.Stdin, os.Stdout, func(line string) ([]string, error) {
		p, err := toLibrary(*to, line)
		return []string{p}, err
	}))
}

// toLibrary writes the item at path into the library dir and returns the
// path of the book it wrote.
func toLibrary(dir, path string) (string, error) {
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
		return folder.Put(dir, stem+".pdf", data, m)
	case ".html":
		title := m.Title
		if title == "" {
			title = stem
		}
		book, err := epub.Build(epub.Book{Title: title, Source: m.Source, Modified: m.Fetched}, bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		return folder.Put(dir, stage.Slug(title)+".epub", book, m)
	}
	return "", errors.New("not an HTML or PDF item")
}
