// Command ink-readable strips each HTML item read from stdin down to its
// article, and passes any other item through untouched.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"

	"codeberg.org/readeck/go-readability/v2"

	"github.com/offware-apps/ink-drop/folder"
	"github.com/offware-apps/ink-drop/internal/stage"
)

func main() {
	to := flag.String("to", "", "output folder (default: the user cache folder)")
	flag.Parse()
	dir, err := stage.Dir(*to, "readable")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ink-readable:", err)
		os.Exit(2)
	}
	os.Exit(stage.Run("ink-readable", os.Stdin, os.Stdout, func(line string) ([]string, error) {
		p, err := readable(dir, line)
		return []string{p}, err
	}))
}

// readable writes the article of the HTML item at path into dir and returns
// its path, or returns path itself for an item that is not HTML.
func readable(dir, path string) (string, error) {
	m, err := folder.Get(path)
	if err != nil {
		return "", err
	}
	if filepath.Ext(path) != ".html" {
		return path, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	src, err := url.Parse(m.Source)
	if err != nil {
		return "", err
	}
	a, err := readability.FromReader(f, src)
	if err != nil {
		return "", err
	}
	if a.Node == nil {
		return "", errors.New("no article found")
	}
	var b bytes.Buffer
	b.WriteString("<!DOCTYPE html>\n<html")
	if lang := a.Language(); lang != "" {
		fmt.Fprintf(&b, ` lang="%s"`, html.EscapeString(lang))
	}
	fmt.Fprintf(&b, "><head><meta charset=\"utf-8\"><title>%s</title></head><body>", html.EscapeString(a.Title()))
	if err := a.RenderHTML(&b); err != nil {
		return "", err
	}
	b.WriteString("</body></html>\n")
	m.Title, m.Stage = a.Title(), "ink-readable"
	return folder.Put(dir, filepath.Base(path), b.Bytes(), m)
}
