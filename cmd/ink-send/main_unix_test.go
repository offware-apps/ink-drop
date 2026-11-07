//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/offware-apps/ink-drop/folder"
)

// A sidecar that is a link, a pipe or too large gives no title, and never stops
// the list: only a plain file of a sidecar's size is read.
func TestSidecarOnlyPlainFiles(t *testing.T) {
	f := setup(t)
	outside := filepath.Join(t.TempDir(), "secret.json")
	put(t, outside, `{"schema":1,"file":"link.epub","title":"SECRET"}`)
	side := filepath.Join(f.Library, folder.SidecarDir)
	os.Mkdir(side, 0o755)
	put(t, filepath.Join(f.Library, "link.epub"), "l")
	os.Symlink(outside, filepath.Join(side, "link.epub.json"))
	put(t, filepath.Join(f.Library, "pipe.epub"), "p")
	if err := syscall.Mkfifo(filepath.Join(side, "pipe.epub.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.Library, "big.epub"), "b")
	// Valid JSON even when cut at the cap, so only the cap keeps its title out.
	put(t, filepath.Join(side, "big.epub.json"), `{"schema":1,"file":"big.epub","title":"BIG"}`+strings.Repeat(" ", maxSidecar))
	done := make(chan []book, 1)
	go func() {
		books, _, _ := f.list()
		done <- books
	}()
	select {
	case books := <-done:
		if len(books) != 3 {
			t.Errorf("listed %+v", books)
		}
		for _, b := range books {
			if b.Title != "" {
				t.Errorf("%s took the title %q", b.Name, b.Title)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a pipe sidecar stopped the list")
	}
}
