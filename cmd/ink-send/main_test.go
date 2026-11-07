package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
		Library: filepath.Join(root, "library"),
		Sync:    filepath.Join(root, "sync"),
	}
	for _, d := range []string{f.Library, f.Sync} {
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
	put(t, path, `{"library":"~/Books","sync":"~/Sync/books"}`)
	f, err := load(path)
	if err != nil || f.Library != "/home/reader/Books" || f.Sync != "/home/reader/Sync/books" {
		t.Errorf("got %+v, %v", f, err)
	}
	for _, bad := range []string{
		`{"library":"Books"}`,
		`{}`,
		`{"library":"/Books","sync":"out"}`,
	} {
		put(t, path, bad)
		if _, err := load(path); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

func TestSendCopiesTheBook(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.Library, "a.epub"), "a")
	for range 2 {
		if err := f.send("a.epub"); err != nil {
			t.Fatal(err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(f.Sync, "a.epub")); err != nil || string(b) != "a" {
		t.Errorf("the sync folder holds %q, %v", b, err)
	}
	if entries, _ := os.ReadDir(f.Sync); len(entries) != 1 {
		t.Errorf("a temporary file stayed behind: %v", entries)
	}
}

// With no sync folder set, or one that is not there, nothing is sent and no
// folder is made; an unsend and a delete have nothing to take from it.
func TestNoSyncFolder(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.Library, "a.epub"), "a")
	gone := filepath.Join(t.TempDir(), "usb", "books")
	for _, sync := range []string{"", gone} {
		f.Sync = sync
		if err := f.send("a.epub"); err == nil || exists(gone) {
			t.Errorf("sync %q: a send went through, made %v", sync, exists(gone))
		}
		if err := f.unsend("a.epub"); (err != nil) != (sync != "") {
			t.Errorf("sync %q: an unsend: %v", sync, err)
		}
		if books, _, err := f.list(); err != nil || len(books) != 1 || books[0].Sent {
			t.Errorf("sync %q: list %+v, %v", sync, books, err)
		}
	}
	// A sync folder that is set but not there refuses a delete, which would leave
	// its copy with nothing to take it back; with none set the delete goes ahead.
	if err := f.remove("a.epub"); err == nil || !exists(filepath.Join(f.Library, "a.epub")) {
		t.Errorf("a delete with the sync folder not there: %v", err)
	}
	f.Sync = ""
	if err := f.remove("a.epub"); err != nil || exists(filepath.Join(f.Library, "a.epub")) {
		t.Errorf("a delete with no sync folder set: %v", err)
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
		if err := f.send(name); err == nil {
			t.Errorf("send(%q) was accepted", name)
		}
		if err := f.remove(name); err == nil {
			t.Errorf("remove(%q) was accepted", name)
		}
	}
	for _, name := range []string{"", "..", "../secret.epub", "../library/notes.txt", "notes.txt", ".hidden.epub"} {
		if err := f.unsend(name); err == nil {
			t.Errorf("unsend(%q) was accepted", name)
		}
	}
	for _, kept := range []string{filepath.Join(root, "secret.epub"), filepath.Join(f.Library, "notes.txt"), filepath.Join(f.Library, ".hidden.epub"), filepath.Join(f.Library, "link.epub")} {
		if !exists(kept) {
			t.Errorf("%s is gone", kept)
		}
	}
	if entries, _ := os.ReadDir(f.Sync); len(entries) != 0 {
		t.Errorf("the sync folder holds %v", entries)
	}
}

func TestUnsendKeepsTheBook(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.Library, "a.epub"), "a")
	if err := f.send("a.epub"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := f.unsend("a.epub"); err != nil {
			t.Fatal(err)
		}
	}
	if exists(filepath.Join(f.Sync, "a.epub")) || !exists(filepath.Join(f.Library, "a.epub")) {
		t.Error("unsend did not take the book out of the sync folder alone")
	}
}

func TestRemoveTakesEveryCopy(t *testing.T) {
	f := setup(t)
	path, err := folder.Put(f.Library, "a.epub", []byte("a"), folder.Meta{Source: "https://example.org/a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.send("a.epub"); err != nil {
		t.Fatal(err)
	}
	if err := f.remove("a.epub"); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{path, filepath.Join(f.Sync, "a.epub"), filepath.Join(f.Library, folder.SidecarDir, "a.epub.json")} {
		if exists(gone) {
			t.Errorf("%s is still there", gone)
		}
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
	if err := f.send("plain.pdf"); err != nil {
		t.Fatal(err)
	}
	books, more, err := f.list()
	if err != nil || more != 0 || len(books) != 2 {
		t.Fatalf("got %+v, more %d, %v", books, more, err)
	}
	if books[0].Name != "plain.pdf" || !books[0].Sent || books[0].Title != "" || books[0].Size != 1 {
		t.Errorf("newest book is %+v", books[0])
	}
	if b := books[1]; b.Name != "with-sidecar.epub" || b.Sent || b.Title != "A title" || b.Source != "https://example.org/a" {
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
	put(t, filepath.Join(f.Library, "a.epub"), "a")
	if res := answer(message(`{"action":"list"}`), config); !res.OK || len(res.Books) != 1 || res.Books[0].Name != "a.epub" {
		t.Errorf("list: %+v", res)
	}
	if res := answer(message(`{"action":"send","file":"a.epub"}`), config); !res.OK || !exists(filepath.Join(f.Sync, "a.epub")) {
		t.Errorf("send: %+v", res)
	}
	if res := answer(message(`{"action":"unsend","file":"a.epub"}`), config); !res.OK || exists(filepath.Join(f.Sync, "a.epub")) {
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

// A stage's own folder: listed, sent to the same folder in the sync folder, and
// its sidecar found.
func TestBooksInAFolder(t *testing.T) {
	f := setup(t)
	if _, err := folder.Put(filepath.Join(f.Library, "reviews"), "r.epub", []byte("r"), folder.Meta{Title: "R"}); err != nil {
		t.Fatal(err)
	}
	if err := f.send("reviews/r.epub"); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(f.Sync, "reviews", "r.epub")
	books, _, err := f.list()
	if err != nil || len(books) != 1 || books[0].Name != "reviews/r.epub" || books[0].Title != "R" || !books[0].Sent || !exists(out) {
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

// A book replaced in the library reaches the sync folder on the next send: a link
// keeps the old file.
func TestSendReplacesAnOlderBook(t *testing.T) {
	f := setup(t)
	if _, err := folder.Put(f.Library, "a.epub", []byte("old"), folder.Meta{}); err != nil {
		t.Fatal(err)
	}
	if err := f.send("a.epub"); err != nil {
		t.Fatal(err)
	}
	if _, err := folder.Put(f.Library, "a.epub", []byte("new"), folder.Meta{}); err != nil {
		t.Fatal(err)
	}
	if err := f.send("a.epub"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.Sync, "a.epub")); string(b) != "new" {
		t.Errorf("the sync folder holds %q", b)
	}
}

// The design's own guard: no setting or message can start a program or open a
// connection if the program holds no code for either, directly or below it.
func TestCannotLaunchOrConnect(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ink-send")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	out, err := exec.Command("go", "tool", "nm", bin).Output()
	if err != nil {
		t.Fatal(err)
	}
	// Whatever package it came through, the code to start a program or open a
	// connection would show as one of these symbols in the program itself.
	held := regexp.MustCompile(`\s(os\.StartProcess|os/exec\.|(syscall|golang\.org/x/sys/unix)\.(forkExec|ForkExec|StartProcess|Exec|[Ss]ocket|[Cc]onnect)\b|net\.|plugin\.)`)
	for _, line := range strings.Split(string(out), "\n") {
		if m := held.FindString(line); m != "" {
			t.Errorf("ink-send holds %s", strings.TrimSpace(m))
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
	if err != nil || !exists(f.Library) || f.Library != filepath.Join(home, "Downloads", "ink-drop") {
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

// A sidecar naming another book gives this one no title.
func TestSidecarNamesItsBook(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.Library, "a.epub"), "a")
	os.Mkdir(filepath.Join(f.Library, folder.SidecarDir), 0o755)
	put(t, filepath.Join(f.Library, folder.SidecarDir, "a.epub.json"), `{"schema":1,"file":"b.epub","title":"Not a"}`)
	if books, _, err := f.list(); err != nil || len(books) != 1 || books[0].Title != "" {
		t.Errorf("got %+v, %v", books, err)
	}
}

// The library and the sync folder may not overlap: unticking would delete
// library books.
func TestFoldersMustNotOverlap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "folders.json")
	for _, bad := range []string{
		`{"library":"/b","sync":"/b"}`,
		`{"library":"/b","sync":"/b/out"}`,
		`{"library":"/b/lib","sync":"/b"}`,
	} {
		put(t, path, bad)
		if _, err := load(path); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	put(t, path, `{"library":"/b","sync":"/s"}`)
	if _, err := load(path); err != nil {
		t.Errorf("separate folders: %v", err)
	}
}

// Only a plain book file is listed, never a link or a folder of that name, and a
// sidecar of another schema gives no title.
func TestListOnlyPlainBooks(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.Library, "a.epub"), "a")
	os.Symlink(filepath.Join(f.Library, "a.epub"), filepath.Join(f.Library, "link.epub"))
	os.Mkdir(filepath.Join(f.Library, "dir.epub"), 0o755)
	os.Mkdir(filepath.Join(f.Library, folder.SidecarDir), 0o755)
	put(t, filepath.Join(f.Library, folder.SidecarDir, "a.epub.json"), `{"schema":99,"file":"a.epub","title":"Old"}`)
	books, _, err := f.list()
	if err != nil || len(books) != 1 || books[0].Name != "a.epub" || books[0].Title != "" {
		t.Errorf("got %+v, %v", books, err)
	}
}

// A send that cannot land leaves no temporary file behind.
func TestFailedSendLeavesNoTemp(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.Library, "a.epub"), "a")
	os.Mkdir(filepath.Join(f.Sync, "a.epub"), 0o755)
	put(t, filepath.Join(f.Sync, "a.epub", "keep"), "k")
	if err := f.send("a.epub"); err == nil {
		t.Error("a send replaced a folder")
	}
	if entries, _ := os.ReadDir(f.Sync); len(entries) != 1 {
		t.Errorf("the sync folder holds %v", entries)
	}
}

// A route never overlaps a folder ink-send writes to, so no book lands in it.
func TestRoutesDoNotOverlapFolders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "folders.json")
	for _, bad := range []string{
		`{"library":"/b","routes":[{"path":"/b/notes"}]}`,
		`{"library":"/b/lib","routes":[{"path":"/b"}]}`,
		`{"library":"/b","sync":"/s","routes":[{"path":"/s/notes"}]}`,
	} {
		put(t, path, bad)
		if _, err := load(path); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

// A library book whose name starts like a route id is still sent as a book.
func TestBookNamedLikeARoute(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.Library, "route:x.epub"), "r")
	if _, err := f.handle(request{Action: "send", File: "route:x.epub"}); err != nil || !exists(filepath.Join(f.Sync, "route:x.epub")) {
		t.Errorf("send: %v", err)
	}
}

// Untick takes back only a book the library holds: a file put in the sync folder
// by hand stays.
func TestUnsendOnlyLibraryBooks(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.Sync, "mine.epub"), "mine")
	if _, err := f.handle(request{Action: "unsend", File: "mine.epub"}); err == nil || !exists(filepath.Join(f.Sync, "mine.epub")) {
		t.Errorf("a file the library never held was taken: %v", err)
	}
	put(t, filepath.Join(f.Library, "a.epub"), "a")
	f.send("a.epub")
	if _, err := f.handle(request{Action: "unsend", File: "a.epub"}); err != nil || exists(filepath.Join(f.Sync, "a.epub")) {
		t.Errorf("a library book was not taken back: %v", err)
	}
}

// Untick refuses a name the library does not hold as a plain book, through a
// linked folder included.
func TestUnsendRefusesLinkedLibraryNames(t *testing.T) {
	f := setup(t)
	os.Mkdir(filepath.Join(f.Library, "real"), 0o755)
	put(t, filepath.Join(f.Library, "real", "x.epub"), "x")
	os.Symlink("real", filepath.Join(f.Library, "linkdir"))
	os.Mkdir(filepath.Join(f.Sync, "linkdir"), 0o755)
	put(t, filepath.Join(f.Sync, "linkdir", "x.epub"), "by hand")
	os.Mkdir(filepath.Join(f.Library, "dir.epub"), 0o755)
	put(t, filepath.Join(f.Sync, "dir.epub"), "by hand")
	for _, name := range []string{"linkdir/x.epub", "dir.epub"} {
		if _, err := f.handle(request{Action: "unsend", File: name}); err == nil || !exists(filepath.Join(f.Sync, name)) {
			t.Errorf("untick of %s: %v", name, err)
		}
	}
}

// A link to another folder inside the sync folder never lets two books share one
// copy: a send through it is refused, and nothing through it counts as sent.
func TestSyncFolderLinkInside(t *testing.T) {
	f := setup(t)
	for _, d := range []string{"a", "b"} {
		os.Mkdir(filepath.Join(f.Library, d), 0o755)
		put(t, filepath.Join(f.Library, d, "x.epub"), d)
	}
	os.Mkdir(filepath.Join(f.Sync, "b"), 0o755)
	os.Symlink("b", filepath.Join(f.Sync, "a"))
	if err := f.send("b/x.epub"); err != nil {
		t.Fatal(err)
	}
	if err := f.send("a/x.epub"); err == nil {
		t.Error("a send went through a link inside the sync folder")
	}
	if b, _ := os.ReadFile(filepath.Join(f.Sync, "b", "x.epub")); string(b) != "b" {
		t.Errorf("b's copy holds %q", b)
	}
	books, _, _ := f.list()
	for _, bk := range books {
		if bk.Name == "a/x.epub" && bk.Sent {
			t.Error("a book seen through a link counts as sent")
		}
	}
	if err := f.unsend("a/x.epub"); err == nil || !exists(filepath.Join(f.Sync, "b", "x.epub")) {
		t.Errorf("an unsend through a link: %v", err)
	}
}

// A sidecar that cannot be removed stops a delete, so no later book takes its title.
func TestDeleteNeedsTheSidecarGone(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.Library, "a.epub"), "a")
	os.MkdirAll(filepath.Join(f.Library, folder.SidecarDir, "a.epub.json", "inside"), 0o755)
	if err := f.remove("a.epub"); err == nil {
		t.Error("a delete left its sidecar behind")
	}
}

// Deleting a book never removes another book's sidecar through a linked folder.
func TestDeleteKeepsOtherSidecars(t *testing.T) {
	f := setup(t)
	if _, err := folder.Put(f.Library, "x.epub", []byte("x"), folder.Meta{Title: "Root"}); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(f.Library, "sub"), 0o755)
	put(t, filepath.Join(f.Library, "sub", "x.epub"), "s")
	os.Symlink("../"+folder.SidecarDir, filepath.Join(f.Library, "sub", folder.SidecarDir))
	if err := f.remove("sub/x.epub"); err == nil {
		t.Error("a delete went through a linked sidecar folder")
	}
	if !exists(filepath.Join(f.Library, folder.SidecarDir, "x.epub.json")) {
		t.Error("another book's sidecar is gone")
	}
}

// Two routes that overlap, or a setting this version does not know, are refused.
func TestLoadRefusesOverlapAndUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "folders.json")
	for _, bad := range []string{
		`{"library":"/b","routes":[{"path":"/n"},{"path":"/n/r"}]}`,
		`{"library":"/b","downloads":"/d"}`,
		`{"library":"/b","transmitters":[]}`,
	} {
		put(t, path, bad)
		if _, err := load(path); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}
