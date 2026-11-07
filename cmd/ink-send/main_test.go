package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/offware-apps/ink-drop/folder"
)

func message(body string) *bytes.Buffer {
	var b bytes.Buffer
	binary.Write(&b, binary.NativeEndian, uint32(len(body)))
	b.WriteString(body)
	return &b
}

func setup(t *testing.T) folders {
	t.Helper()
	root := t.TempDir()
	f := folders{
		Library: filepath.Join(root, "library"), Downloads: filepath.Join(root, "downloads"),
		Transmitters: []transmitter{{Name: "Tablet", Folder: filepath.Join(root, "outbox")}},
	}
	for _, d := range []string{f.Library, f.Transmitters[0].Folder, f.Downloads} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestRead(t *testing.T) {
	if b, err := read(message(`{"action":"list"}`)); err != nil || string(b) != `{"action":"list"}` {
		t.Errorf("the extension's message read as %q, %v", b, err)
	}
	if _, err := read(message(strings.Repeat(" ", 1024))); err != nil {
		t.Errorf("a message of exactly 1 KB was refused: %v", err)
	}
	// One byte over the cap, and otherwise a whole message.
	if _, err := read(message(strings.Repeat(" ", 1025))); err == nil {
		t.Error("a message over 1 KB was accepted")
	}
	short := message("{}")
	short.Truncate(5)
	if _, err := read(short); err == nil {
		t.Error("a message shorter than its length was accepted")
	}
}

func TestWrite(t *testing.T) {
	var b bytes.Buffer
	if err := write(&b, reply{OK: true}); err != nil {
		t.Fatal(err)
	}
	var n uint32
	binary.Read(&b, binary.NativeEndian, &n)
	if int(n) != b.Len() || b.String() != `{"ok":true}` {
		t.Errorf("length %d, body %q", n, b.String())
	}
}

func TestLoad(t *testing.T) {
	t.Setenv("HOME", "/home/reader")
	dir := t.TempDir()
	path := filepath.Join(dir, "folders.json")
	if _, err := load(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("a missing config file gave %v", err)
	}
	put(t, path, `{"library":"~/Books","transmitters":[{"name":"T","folder":"~/out"},{"name":"U","folder":"/mnt/out"}]}`)
	f, err := load(path)
	if err != nil || f.Library != "/home/reader/Books" || f.Downloads != "" || f.Transmitters[0].Folder != "/home/reader/out" ||
		f.Transmitters[1].Folder != "/mnt/out" {
		t.Errorf("got %+v, %v", f, err)
	}
	for _, bad := range []string{
		`{"library":"Books"}`,
		`{"library":"/Books","transmitters":[{"name":"T","folder":"out"}]}`,
		`{"library":"/Books","transmitters":[{"folder":"/out"}]}`,
		`{"library":"/Books","downloads":"dl"}`,
	} {
		put(t, path, bad)
		if _, err := load(path); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

func TestSendLinksTheBook(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.Library, "a.epub"), "a")
	for range 2 {
		if err := f.send("a.epub", "Tablet"); err != nil {
			t.Fatal(err)
		}
	}
	lib, _ := os.Stat(filepath.Join(f.Library, "a.epub"))
	out, err := os.Stat(filepath.Join(f.Transmitters[0].Folder, "a.epub"))
	if err != nil || !os.SameFile(lib, out) {
		t.Errorf("the outbox holds no link to the book: %v", err)
	}
}

// A name comes from the browser, so each of these must leave every file alone.
func TestBadNamesAreRefused(t *testing.T) {
	f := setup(t)
	root := filepath.Dir(f.Library)
	put(t, filepath.Join(root, "secret.epub"), "secret")
	put(t, filepath.Join(f.Library, "notes.txt"), "notes")
	put(t, filepath.Join(f.Library, ".hidden.epub"), "hidden")
	if err := os.Symlink(filepath.Join(root, "secret.epub"), filepath.Join(f.Library, "link.epub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.Library, "dir.epub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A folder that is a link would take a name out of the library, as would a
	// second level or a hidden folder holding a sidecar.
	if err := os.Mkdir(filepath.Join(root, "outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(root, "outside", "secret.epub"), "secret")
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(f.Library, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.Library, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.Library, "a", "b", "deep.epub"), "deep")
	if err := os.Mkdir(filepath.Join(f.Library, ".ink"), 0o755); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.Library, ".ink", "side.epub"), "side")
	// Only the first of these gets past every check but the one on a path.
	for _, name := range []string{"sub/../../secret.epub", "", ".", "..", "../secret.epub", "/etc/passwd", "sub/a.epub", "notes.txt", ".hidden.epub", "link.epub", "dir.epub", "missing.epub", "linked/secret.epub", "a/b/deep.epub", ".ink/side.epub", "/secret.epub"} {
		if err := f.send(name, "Tablet"); err == nil {
			t.Errorf("send(%q) was accepted", name)
		}
		if err := f.remove(name); err == nil {
			t.Errorf("remove(%q) was accepted", name)
		}
	}
	for _, name := range []string{"", "..", "../secret.epub", "../library/notes.txt", "notes.txt", ".hidden.epub"} {
		if err := f.unsend(name, "Tablet"); err == nil {
			t.Errorf("unsend(%q) was accepted", name)
		}
	}
	for _, kept := range []string{filepath.Join(root, "secret.epub"), filepath.Join(f.Library, "notes.txt"), filepath.Join(f.Library, ".hidden.epub"), filepath.Join(f.Library, "link.epub")} {
		if !exists(kept) {
			t.Errorf("%s is gone", kept)
		}
	}
	if entries, _ := os.ReadDir(f.Transmitters[0].Folder); len(entries) != 0 {
		t.Errorf("the outbox holds %v", entries)
	}
}

func TestUnsendKeepsTheBook(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.Library, "a.epub"), "a")
	if err := f.send("a.epub", "Tablet"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := f.unsend("a.epub", "Tablet"); err != nil {
			t.Fatal(err)
		}
	}
	if exists(filepath.Join(f.Transmitters[0].Folder, "a.epub")) || !exists(filepath.Join(f.Library, "a.epub")) {
		t.Error("unsend did not take the book out of the outbox alone")
	}
}

func TestRemoveTakesEveryCopy(t *testing.T) {
	f := setup(t)
	path, err := folder.Put(f.Library, "a.epub", []byte("a"), folder.Meta{Source: "https://example.org/a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.send("a.epub", "Tablet"); err != nil {
		t.Fatal(err)
	}
	if err := f.remove("a.epub"); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{path, filepath.Join(f.Transmitters[0].Folder, "a.epub"), filepath.Join(f.Library, folder.SidecarDir, "a.epub.json")} {
		if exists(gone) {
			t.Errorf("%s is still there", gone)
		}
	}
}

func TestCollect(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.Library, "same.epub"), "old")
	put(t, filepath.Join(f.Library, "same-1.epub"), "old too")
	put(t, filepath.Join(f.Downloads, "new.epub"), "new")
	put(t, filepath.Join(f.Downloads, "same.epub"), "second")
	put(t, filepath.Join(f.Downloads, "writing.epub"), "")
	put(t, filepath.Join(f.Downloads, "new.epub.part"), "partial")
	moved, err := f.collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || moved["same.epub"] != "same-2.epub" {
		t.Errorf("moved %v", moved)
	}
	if b, _ := os.ReadFile(filepath.Join(f.Library, "same.epub")); string(b) != "old" {
		t.Errorf("a collected book overwrote one in the library: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(f.Library, "same-2.epub")); string(b) != "second" {
		t.Errorf("same-2.epub holds %q", b)
	}
	if !exists(filepath.Join(f.Library, "new.epub")) || exists(filepath.Join(f.Downloads, "new.epub")) {
		t.Error("new.epub did not move")
	}
	if exists(filepath.Join(f.Library, "writing.epub")) || !exists(filepath.Join(f.Downloads, "writing.epub")) {
		t.Error("an empty file, still downloading, moved")
	}
	if exists(filepath.Join(f.Library, "new.epub.part")) {
		t.Error("a partial file moved")
	}
}

func TestCollectWithoutDownloads(t *testing.T) {
	f := setup(t)
	f.Downloads = ""
	if moved, err := f.collect(); err != nil || moved != nil {
		t.Errorf("no downloads folder set: %v, %v", moved, err)
	}
	f.Downloads = filepath.Join(f.Library, "missing")
	if moved, err := f.collect(); err != nil || moved != nil {
		t.Errorf("a missing downloads folder: %v, %v", moved, err)
	}
}

func TestList(t *testing.T) {
	f := setup(t)
	if _, err := folder.Put(f.Library, "with-sidecar.epub", []byte("aa"), folder.Meta{Title: "A title", Source: "https://example.org/a"}); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.Library, "plain.pdf"), "p")
	put(t, filepath.Join(f.Library, "notes.txt"), "n")
	now := time.Now()
	os.Chtimes(filepath.Join(f.Library, "with-sidecar.epub"), now, now.Add(-time.Hour))
	os.Chtimes(filepath.Join(f.Library, "plain.pdf"), now, now)
	if err := f.send("plain.pdf", "Tablet"); err != nil {
		t.Fatal(err)
	}
	books, more, err := f.list()
	if err != nil || more != 0 || len(books) != 2 {
		t.Fatalf("got %+v, more %d, %v", books, more, err)
	}
	if books[0].Name != "plain.pdf" || len(books[0].Sent) != 1 || books[0].Sent[0] != "Tablet" || books[0].Title != "" || books[0].Size != 1 {
		t.Errorf("newest book is %+v", books[0])
	}
	if b := books[1]; b.Name != "with-sidecar.epub" || b.Sent != nil || b.Title != "A title" || b.Source != "https://example.org/a" {
		t.Errorf("older book is %+v", b)
	}
}

// A browser refuses a host's message over 1 MB.
func TestListFitsInOneMessage(t *testing.T) {
	f := setup(t)
	long := strings.Repeat("é", 2*maxText)
	const total = 1200
	for i := range total {
		name := fmt.Sprintf("%0200d.epub", i)
		if _, err := folder.Put(f.Library, name, []byte("a"), folder.Meta{Title: long, Source: long}); err != nil {
			t.Fatal(err)
		}
	}
	books, more, err := f.list()
	if err != nil || more == 0 || len(books)+more != total {
		t.Fatalf("%d books, more %d, %v", len(books), more, err)
	}
	if n := len([]rune(books[0].Title)); n != maxText {
		t.Errorf("a title of %d characters", n)
	}
	var b bytes.Buffer
	if err := write(&b, reply{OK: true, Books: books, More: more}); err != nil {
		t.Fatal(err)
	}
	if b.Len() > 1<<20 {
		t.Errorf("a message of %d bytes", b.Len())
	}
}

func TestAnswer(t *testing.T) {
	f := setup(t)
	config := filepath.Join(t.TempDir(), "folders.json")
	c, _ := json.Marshal(f)
	put(t, config, string(c))
	put(t, filepath.Join(f.Downloads, "a.epub"), "a")
	if res := answer(message(`{"action":"list"}`), config); !res.OK || len(res.Books) != 1 || res.Books[0].Name != "a.epub" {
		t.Errorf("list: %+v", res)
	}
	if res := answer(message(`{"action":"list"}`), config); len(res.Transmitters) != 1 || res.Transmitters[0] != "Tablet" {
		t.Errorf("list names no transmitter: %+v", res)
	}
	if res := answer(message(`{"action":"send","file":"a.epub","to":"Nobody"}`), config); res.OK || exists(filepath.Join(f.Transmitters[0].Folder, "a.epub")) {
		t.Errorf("send to a transmitter nobody wrote: %+v", res)
	}
	if res := answer(message(`{"action":"send","file":"a.epub","to":"Tablet"}`), config); !res.OK || !exists(filepath.Join(f.Transmitters[0].Folder, "a.epub")) {
		t.Errorf("send: %+v", res)
	}
	if res := answer(message(`{"action":"unsend","file":"a.epub","to":"Tablet"}`), config); !res.OK || exists(filepath.Join(f.Transmitters[0].Folder, "a.epub")) {
		t.Errorf("unsend: %+v", res)
	}
	if res := answer(message(`{"action":"delete","file":"a.epub"}`), config); !res.OK || exists(filepath.Join(f.Library, "a.epub")) {
		t.Errorf("delete: %+v", res)
	}
	for _, bad := range []string{`{"action":"format"}`, `{}`, `not json`} {
		if res := answer(message(bad), config); res.OK || res.Error == "" {
			t.Errorf("%s: %+v", bad, res)
		}
	}
}

// A stage's own folder: listed, sent to the same folder in the transmitter, and
// its sidecar found.
func TestBooksInAFolder(t *testing.T) {
	f := setup(t)
	if _, err := folder.Put(filepath.Join(f.Library, "reviews"), "r.epub", []byte("r"), folder.Meta{Title: "R"}); err != nil {
		t.Fatal(err)
	}
	if err := f.send("reviews/r.epub", "Tablet"); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(f.Transmitters[0].Folder, "reviews", "r.epub")
	books, _, err := f.list()
	if err != nil || len(books) != 1 || books[0].Name != "reviews/r.epub" || books[0].Title != "R" || len(books[0].Sent) != 1 || !exists(out) {
		t.Fatalf("got %+v, %v", books, err)
	}
	if err := f.remove("reviews/r.epub"); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{out, filepath.Join(f.Library, "reviews", "r.epub"), filepath.Join(f.Library, "reviews", folder.SidecarDir, "r.epub.json")} {
		if exists(gone) {
			t.Errorf("%s is still there", gone)
		}
	}
}

func TestEveryTransmitterIsItsOwn(t *testing.T) {
	f := setup(t)
	second := filepath.Join(t.TempDir(), "usb")
	f.Transmitters = append(f.Transmitters, transmitter{Name: "USB", Folder: second})
	put(t, filepath.Join(f.Library, "a.epub"), "a")
	if err := f.send("a.epub", "USB"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(f.Transmitters[0].Folder, "a.epub")) || !exists(filepath.Join(second, "a.epub")) {
		t.Error("the book went to the wrong transmitter")
	}
	if err := f.unsend("a.epub", "Tablet"); err != nil || !exists(filepath.Join(second, "a.epub")) {
		t.Errorf("unsend from one took it from the other: %v", err)
	}
	if err := f.remove("a.epub"); err != nil || exists(filepath.Join(second, "a.epub")) {
		t.Errorf("remove left a copy behind: %v", err)
	}
}

// A book replaced in the library reaches the transmitter on the next send: a link
// keeps the old file.
func TestSendReplacesAnOlderBook(t *testing.T) {
	f := setup(t)
	if _, err := folder.Put(f.Library, "a.epub", []byte("old"), folder.Meta{}); err != nil {
		t.Fatal(err)
	}
	if err := f.send("a.epub", "Tablet"); err != nil {
		t.Fatal(err)
	}
	if _, err := folder.Put(f.Library, "a.epub", []byte("new"), folder.Meta{}); err != nil {
		t.Fatal(err)
	}
	if err := f.send("a.epub", "Tablet"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.Transmitters[0].Folder, "a.epub")); string(b) != "new" {
		t.Errorf("the transmitter holds %q", b)
	}
}

// The design's own guard: no setting or message can start a program or open a
// connection if the program holds no code for either, directly or below it.
func TestCannotLaunchOrConnect(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		switch pkg {
		case "os/exec", "net", "net/http", "plugin", "syscall/js":
			t.Errorf("ink-send depends on %s", pkg)
		}
	}
}

func TestOldActionsAreGone(t *testing.T) {
	f := setup(t)
	for _, action := range []string{"start", "update", "refresh"} {
		if _, err := f.handle(request{Action: action}); err == nil {
			t.Errorf("%s was accepted", action)
		}
	}
}

func TestWriteDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "conf", "folders.json")
	if err := writeDefaults(path); err != nil {
		t.Fatal(err)
	}
	f, err := load(path)
	if err != nil || !exists(f.Library) || f.Library != filepath.Join(home, "Books") {
		t.Errorf("defaults gave %+v, %v", f, err)
	}
	if info, err := os.Stat(f.Library); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("a new library others can read: %v, %v", info.Mode(), err)
	}
	put(t, path, `{"library":"/mine"}`)
	if err := writeDefaults(path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != `{"library":"/mine"}` {
		t.Errorf("an existing config file became %s", b)
	}
}

func TestRegister(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := register()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Name, Path, Type  string
		AllowedExtensions []string `json:"allowed_extensions"`
	}
	if err := json.Unmarshal(b, &m); err != nil || m.Name != hostName || m.Type != "stdio" || !filepath.IsAbs(m.Path) ||
		len(m.AllowedExtensions) != 1 || m.AllowedExtensions[0] != extensionID || filepath.Base(path) != hostName+".json" {
		t.Errorf("manifest %s: %+v, %v", path, m, err)
	}
}
