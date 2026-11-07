//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A book is written readable by a sync tool whatever the umask.
func TestBooksAreReadableBySync(t *testing.T) {
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })
	notes := t.TempDir()
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	f := withRoutes(t, route{Path: notes})
	if err := f.sendRoute("route:0:a.md"); err != nil {
		t.Fatal(err)
	}
	it, _ := f.findRoute("route:0:a.md")
	for _, p := range []string{filepath.Join(f.Library, it.tab, it.name), filepath.Join(f.Sync, it.tab, it.name)} {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o644 {
			t.Errorf("%s: %v, %v", p, info.Mode(), err)
		}
	}
}

// A pipe or a link in a route is never read and never stops the list: a folder
// route skips it, and a file or files route naming it is left out.
func TestRoutePipesAndLinks(t *testing.T) {
	home := t.TempDir()
	put(t, filepath.Join(home, "secret.md"), "secret_word\n")
	notes := filepath.Join(home, "notes")
	os.Mkdir(notes, 0o755)
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	if err := syscall.Mkfifo(filepath.Join(notes, "p.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Symlink("a.md", filepath.Join(notes, "alias.md"))
	// A file route that is a link into its own parent folder.
	os.Symlink("secret.md", filepath.Join(home, "todo.md"))
	f := withRoutes(t,
		route{Path: notes},
		route{Path: notes, Files: []string{"a.md", "p.md"}},
		route{Path: notes, Files: []string{"alias.md"}},
		route{Path: filepath.Join(home, "todo.md")},
	)
	done := make(chan map[string]book, 1)
	go func() { done <- listed(t, f) }()
	select {
	case got := <-done:
		if len(got) != 1 || got["route:0:a.md"].Name == "" {
			t.Errorf("listed %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a pipe in a route stopped the list")
	}
	if err := f.sendRoute("route:3:todo.md"); err == nil {
		t.Error("a route that is a link was read")
	}
}

// Unsend takes only a book out of the sync folder, never a folder of that name.
func TestUnsendLeavesFolders(t *testing.T) {
	f := setup(t)
	os.Mkdir(filepath.Join(f.Sync, "x.epub"), 0o755)
	put(t, filepath.Join(f.Sync, "x.epub", "inside"), "i")
	os.Mkdir(filepath.Join(f.Sync, "y.epub"), 0o755)
	for _, name := range []string{"x.epub", "y.epub"} {
		if err := f.unsend(name); err == nil || !exists(filepath.Join(f.Sync, name)) {
			t.Errorf("unsend took the folder %s: %v", name, err)
		}
	}
}

// A folder in the config file that is a pipe is refused, and never stops the
// helper waiting for a writer.
func TestConfigPipesAreRefused(t *testing.T) {
	f := setup(t)
	pipe := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(pipe, 0o644); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.Library, "a.epub"), "a")
	for _, set := range []func(){
		func() { f.Sync = pipe },
		func() { f.Routes = []route{{Path: pipe, Tab: defaultTab}} },
		func() { f.Routes = []route{{Path: pipe, Tab: defaultTab, Files: []string{"a.md"}}} },
	} {
		g := f
		set()
		done := make(chan struct{})
		go func() {
			f.list()
			f.send("a.epub")
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a pipe in the config file stopped the helper")
		}
		f = g
	}
}

// A pipe is never opened: whoever waits to write to it stays waiting.
func TestPipeIsNeverOpened(t *testing.T) {
	dir := t.TempDir()
	pipe := filepath.Join(dir, "p.md")
	if err := syscall.Mkfifo(pipe, 0o644); err != nil {
		t.Fatal(err)
	}
	opened := make(chan struct{})
	go func() {
		w, err := os.OpenFile(pipe, os.O_WRONLY, 0)
		if err == nil {
			w.Close()
		}
		close(opened)
	}()
	// Give the writer time to start waiting before the read.
	time.Sleep(200 * time.Millisecond)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, _, err := readPlain(root, "p.md", 0); err == nil {
		t.Error("a pipe was read")
	}
	select {
	case <-opened:
		t.Error("reading released the pipe's writer")
	case <-time.After(300 * time.Millisecond):
	}
	// Release the writer so the test ends.
	if r, err := os.OpenFile(pipe, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
		<-opened
		r.Close()
	}
}

// A library folder or a route folder that cannot be read is left out, and the
// list still answers with every other book.
func TestUnreadableFoldersAreLeftOut(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not bind root")
	}
	f := setup(t)
	put(t, filepath.Join(f.Library, "a.epub"), "a")
	os.Mkdir(filepath.Join(f.Library, "locked"), 0o755)
	put(t, filepath.Join(f.Library, "locked", "b.epub"), "b")
	os.Chmod(filepath.Join(f.Library, "locked"), 0)
	t.Cleanup(func() { os.Chmod(filepath.Join(f.Library, "locked"), 0o755) })
	shut := t.TempDir()
	put(t, filepath.Join(shut, "n.md"), "# N\n")
	os.Chmod(shut, 0)
	t.Cleanup(func() { os.Chmod(shut, 0o755) })
	open := t.TempDir()
	put(t, filepath.Join(open, "o.md"), "# O\n")
	f.Routes = []route{{Path: shut, Tab: defaultTab}, {Path: open, Tab: defaultTab}}
	books, _, err := f.list()
	if err != nil || len(books) != 2 {
		t.Errorf("got %+v, %v", books, err)
	}
}
