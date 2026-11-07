// Command ink-send is a browser native-messaging host: it lists the library for
// the ink-drop extension, and puts a book in, or takes it out of, the one folder
// a sync tool such as Syncthing sends to the reader. It touches only the folders
// its config file names, each through a root that no name or link can leave. It
// holds no code that starts a program or opens a connection, so it never sends
// anything itself: the sync tool does. `ink-send -install` registers it with
// Firefox for the current user.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/offware-apps/ink-drop/folder"
)

// The names the extension and Firefox use for this host.
const (
	hostName    = "ink_drop"
	extensionID = "ink-drop@offware-apps.github.io"
)

// A browser refuses a host's message over 1 MB, so a list stops at maxList
// bytes, leaving room for the rest of the reply.
const (
	maxList = 900_000
	maxText = 300
	// maxSidecar bounds a sidecar read; a real one is a few hundred bytes.
	maxSidecar = 64 << 10
)

// folders is the config file. Library is the folder the browser saves pages
// into, since an extension can save nowhere else, and Sync the folder the sync
// tool sends to the reader.
type folders struct {
	Library string  `json:"library"`
	Sync    string  `json:"sync,omitempty"`
	Routes  []route `json:"routes,omitempty"`
}

// defaults is the config file -install writes where none exists.
const defaults = `{
  "library": "~/Downloads/ink-drop"
}
`

type request struct {
	Action string `json:"action"`
	File   string `json:"file"`
}

type book struct {
	Name     string `json:"name"`
	Title    string `json:"title,omitempty"`
	Source   string `json:"source,omitempty"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
	// Sent says the sync folder holds the book.
	Sent bool `json:"sent,omitempty"`
	// Folder is the library folder holding it, Where the folder of a route's book
	// inside its route, and Route says the name is a route's id.
	Folder string `json:"folder,omitempty"`
	Where  string `json:"where,omitempty"`
	Route  bool   `json:"route,omitempty"`
}

type reply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Books []book `json:"books,omitempty"`
	More  int    `json:"more,omitempty"`
}

func main() {
	install := flag.Bool("install", false, "register with Firefox for this user, then exit")
	flag.Parse()
	conf, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ink-send:", err)
		os.Exit(1)
	}
	configFile := filepath.Join(conf, "ink-drop", "folders.json")
	if *install {
		path, err := register()
		if err != nil {
			fmt.Fprintln(os.Stderr, "ink-send:", err)
			os.Exit(1)
		}
		if err := writeDefaults(configFile); err != nil {
			fmt.Fprintln(os.Stderr, "ink-send:", err)
			os.Exit(1)
		}
		fmt.Printf("ink-send: registered in %s\nink-send: its folders are in %s\n", path, configFile)
		return
	}
	if err := write(os.Stdout, answer(os.Stdin, configFile)); err != nil {
		fmt.Fprintln(os.Stderr, "ink-send:", err)
		os.Exit(1)
	}
}

// answer returns the reply to the one message on r.
func answer(r io.Reader, configFile string) reply {
	res, err := func() (reply, error) {
		body, err := read(r)
		if err != nil {
			return reply{}, err
		}
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			return reply{}, err
		}
		f, err := load(configFile)
		if err != nil {
			return reply{}, err
		}
		return f.handle(req)
	}()
	if err != nil {
		return reply{Error: err.Error()}
	}
	res.OK = true
	return res
}

// load reads the config file, where a leading ~/ means the home folder.
func load(path string) (folders, error) {
	var f folders
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, fmt.Errorf("%s is missing. Run: ink-send -install", path)
	}
	if err != nil {
		return f, err
	}
	// A setting this version does not know, from an older one for instance, is an
	// error rather than silently ignored.
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return f, err
	}
	for _, p := range []*string{&f.Library, &f.Sync} {
		if rest, ok := strings.CutPrefix(*p, "~/"); ok {
			*p = filepath.Join(home, rest)
		}
		if *p != "" && !filepath.IsAbs(*p) {
			return f, fmt.Errorf("%s: %q needs a full path", path, *p)
		}
	}
	if f.Library == "" {
		return f, fmt.Errorf("%s: the library needs a full path", path)
	}
	// Unticking removes from sync, so it may not hold, or sit inside, the library.
	all := []string{f.Library, f.Sync}
	if f.Sync != "" && (inside(f.Library, f.Sync) || inside(f.Sync, f.Library)) {
		return f, fmt.Errorf("%s: %q and %q overlap", path, f.Library, f.Sync)
	}
	if err := checkRoutes(f.Routes, home); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	// A route is never written, so it may not hold, or sit inside, a folder that is,
	// nor another route, which would offer one note twice.
	for i, r := range f.Routes {
		for _, earlier := range f.Routes[:i] {
			if inside(r.Path, earlier.Path) || inside(earlier.Path, r.Path) {
				return f, fmt.Errorf("%s: routes %q and %q overlap", path, r.Path, earlier.Path)
			}
		}
		for _, x := range all {
			if x != "" && (inside(r.Path, x) || inside(x, r.Path)) {
				return f, fmt.Errorf("%s: route %q and %q overlap", path, r.Path, x)
			}
		}
	}
	return f, nil
}

// inside reports whether path is dir or within it, links resolved where they can be.
func inside(path, dir string) bool {
	rel, err := filepath.Rel(resolve(dir), resolve(path))
	return err == nil && filepath.IsLocal(rel)
}

// resolve returns p with its links followed, or cleaned where it does not exist.
func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// writeDefaults leaves a config file there is alone, and creates the library of
// the one it wrote.
func writeDefaults(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(defaults), 0o644); err != nil {
		return err
	}
	f, err := load(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(f.Library, 0o700)
}

func (f folders) handle(req request) (reply, error) {
	switch req.Action {
	case "list":
		books, more, err := f.list()
		return reply{Books: books, More: more}, err
	case "send":
		if !isBook(req.File) && strings.HasPrefix(req.File, routePrefix) {
			return reply{}, f.sendRoute(req.File)
		}
		return reply{}, f.send(req.File)
	case "unsend":
		if !isBook(req.File) && strings.HasPrefix(req.File, routePrefix) {
			return reply{}, f.unsendRoute(req.File)
		}
		// Only a book the library holds is taken back, so a file put in the sync
		// folder by hand, under a name the library does not hold, is never touched.
		if err := f.inLibrary(req.File); err != nil {
			return reply{}, err
		}
		return reply{}, f.unsend(req.File)
	case "delete":
		return reply{}, f.remove(req.File)
	}
	return reply{}, fmt.Errorf("unknown action %q", req.Action)
}

// isBook reports whether name is a file a reader opens, alone or in one folder
// of the library and never hidden. It shapes a name from the browser; the roots
// are what keep every name inside its folder.
func isBook(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	parts := strings.Split(name, "/")
	if len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return ext == ".epub" || ext == ".pdf"
}

// errNoSync is what a send gets with no sync folder to put the book in.
var errNoSync = errors.New("no sync folder: set sync in the config file to a folder that exists")

// openDir opens a folder named in the config file as a root. A path that is not
// a folder, a pipe for one, is refused before it is opened, since opening a
// pipe waits for a writer.
func openDir(p string) (*os.Root, error) {
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a folder", p)
	}
	return os.OpenRoot(p)
}

// openSync opens the sync folder, or returns nil where none is set or it is not
// there, a drive not mounted for one.
func (f folders) openSync() (*os.Root, error) {
	if f.Sync == "" {
		return nil, nil
	}
	root, err := openDir(f.Sync)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return root, err
}

// openPlain opens the file rel inside root, refusing a link, a pipe or any other
// special file, and a path through a folder that is a link, so no link is ever
// followed. It refuses what is not a plain file, and checks the file it opened,
// without waiting, is the very file it looked at, so one swapped in
// between the look and the open is refused too. Every file ink-send reads, but
// its config file, comes through here.
func openPlain(root *os.Root, rel string) (*os.File, fs.FileInfo, error) {
	if err := plainDirs(root, rel); err != nil {
		return nil, nil, err
	}
	seen, err := root.Lstat(rel)
	if err != nil {
		return nil, nil, err
	}
	// Opening a pipe would release whoever waits to write to it.
	if !seen.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%q is not a plain file", rel)
	}
	file, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err == nil && !os.SameFile(seen, opened) {
		err = fmt.Errorf("%q changed while it was read", rel)
	}
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, opened, nil
}

// plainDirs checks that no folder on the way to rel inside root is a link, so a
// root never follows one, and two names never meet in one file.
func plainDirs(root *os.Root, rel string) error {
	for dir := path.Dir(rel); dir != "." && dir != "/"; dir = path.Dir(dir) {
		info, err := root.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%q is not a plain folder", dir)
		}
	}
	return nil
}

// mkdirPlain makes the folders of dir inside root one by one, refusing one that
// is there and is not a plain folder, so no folder is made through a link.
func mkdirPlain(root *os.Root, dir string) error {
	at := ""
	for _, part := range strings.Split(dir, "/") {
		if part == "" || part == "." {
			continue
		}
		at = path.Join(at, part)
		info, err := root.Lstat(at)
		if errors.Is(err, fs.ErrNotExist) {
			if err := root.Mkdir(at, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%q is not a plain folder", at)
		}
	}
	return nil
}

// removeIn removes name inside root, through no folder that is a link. A name
// that is not there is nothing to remove.
func removeIn(root *os.Root, name string) error {
	err := plainDirs(root, name)
	if err == nil {
		err = root.Remove(name)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// readPlain returns the file rel inside root, at most max bytes of it.
func readPlain(root *os.Root, rel string, max int64) ([]byte, fs.FileInfo, error) {
	file, info, err := openPlain(root, rel)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err == nil && int64(len(data)) > max {
		err = fmt.Errorf("%q is over %d bytes", rel, max)
	}
	return data, info, err
}

// writeIn puts what r holds at name inside root through a hidden temporary file
// and a rename, so a sync tool never sees half a book under its name.
func writeIn(root *os.Root, name string, r io.Reader) error {
	if err := mkdirPlain(root, path.Dir(name)); err != nil {
		return err
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(name), ".ink-tmp-"+hex.EncodeToString(nonce[:]))
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	// The umask would narrow 0o644, and a sync tool reads these files too.
	err = file.Chmod(0o644)
	if err == nil {
		_, err = io.Copy(file, r)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = root.Rename(tmp, name)
	}
	if err != nil {
		root.Remove(tmp)
	}
	return err
}

// send copies the book into the sync folder, replacing an older copy.
func (f folders) send(name string) error {
	if !isBook(name) {
		return fmt.Errorf("%q is not a book name", name)
	}
	lib, err := openDir(f.Library)
	if err != nil {
		return err
	}
	defer lib.Close()
	book, _, err := openPlain(lib, name)
	if err != nil {
		return err
	}
	defer book.Close()
	out, err := f.openSync()
	if err != nil {
		return err
	}
	if out == nil {
		return errNoSync
	}
	defer out.Close()
	return writeIn(out, name, book)
}

// unsend takes the book out of the sync folder.
func (f folders) unsend(name string) error {
	if !isBook(name) {
		return fmt.Errorf("%q is not a book name", name)
	}
	out, err := f.openSync()
	if err != nil {
		return err
	}
	if out == nil {
		if f.Sync != "" {
			return fmt.Errorf("the sync folder %q is not there: connect it first", f.Sync)
		}
		return nil
	}
	defer out.Close()
	info, err := out.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%q is a folder in the sync folder", name)
	}
	return removeIn(out, name)
}

// inLibrary reports, as an error, a name that is not a plain book in the library.
func (f folders) inLibrary(name string) error {
	if !isBook(name) {
		return fmt.Errorf("%q is not a book name", name)
	}
	lib, err := openDir(f.Library)
	if err != nil {
		return err
	}
	defer lib.Close()
	book, _, err := openPlain(lib, name)
	if err != nil {
		return err
	}
	return book.Close()
}

// remove deletes the book, its copy in the sync folder and its sidecar.
func (f folders) remove(name string) error {
	if err := f.inLibrary(name); err != nil {
		return err
	}
	// unsend refuses while the sync folder is not there, so no copy is left behind.
	if err := f.unsend(name); err != nil {
		return err
	}
	lib, err := openDir(f.Library)
	if err != nil {
		return err
	}
	defer lib.Close()
	if err := removeIn(lib, sidecarOf(name)); err != nil {
		return err
	}
	return removeIn(lib, name)
}

func sidecarOf(name string) string {
	return path.Join(path.Dir(name), folder.SidecarDir, path.Base(name)+".json")
}

// names returns every book's name in the library, and in its folders, one level
// down. A folder is opened only as a folder, never through a link.
func names(lib *os.Root) ([]string, error) {
	entries, err := fs.ReadDir(lib.FS(), ".")
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() {
			found = append(found, e.Name())
			continue
		}
		sub, err := lib.OpenRoot(e.Name())
		if err != nil {
			continue
		}
		inside, err := fs.ReadDir(sub.FS(), ".")
		sub.Close()
		if err != nil {
			continue
		}
		for _, in := range inside {
			found = append(found, e.Name()+"/"+in.Name())
		}
	}
	return found, nil
}

// sidecar returns the title and source a book's sidecar holds, read only when it
// is a plain file of a sidecar's size.
func sidecar(lib *os.Root, name string) (folder.Meta, bool) {
	var m folder.Meta
	b, _, err := readPlain(lib, sidecarOf(name), maxSidecar)
	if err != nil || json.Unmarshal(b, &m) != nil || m.Schema != folder.Schema || m.File != path.Base(name) {
		return m, false
	}
	return m, true
}

// list returns the library's newest books, and how many more it left out. A
// title and a source come from the sidecar, where the book has one.
func (f folders) list() ([]book, int, error) {
	lib, err := openDir(f.Library)
	if err != nil {
		return nil, 0, err
	}
	defer lib.Close()
	found, err := names(lib)
	if err != nil {
		return nil, 0, err
	}
	out, err := f.openSync()
	if err != nil {
		return nil, 0, err
	}
	if out != nil {
		defer out.Close()
	}
	sent := func(name string) bool {
		if out == nil || plainDirs(out, name) != nil {
			return false
		}
		_, err := out.Lstat(name)
		return err == nil
	}
	books := []book{}
	made := map[string]bool{}
	for _, it := range f.routeItems() {
		name := it.tab + "/" + it.name
		made[name] = true
		b := book{Name: it.id, Title: clip(it.title), Modified: it.modified, Folder: it.tab, Where: clip(it.where), Route: true, Sent: sent(name)}
		if info, err := lib.Lstat(name); err == nil {
			b.Size = info.Size()
		}
		books = append(books, b)
	}
	for _, name := range found {
		if made[name] {
			continue
		}
		info, err := lib.Lstat(name)
		if err != nil || !isBook(name) || !info.Mode().IsRegular() {
			continue
		}
		b := book{Name: name, Size: info.Size(), Modified: info.ModTime().UnixMilli(), Sent: sent(name)}
		if dir, _, ok := strings.Cut(name, "/"); ok {
			b.Folder = dir
		}
		if m, ok := sidecar(lib, name); ok {
			b.Title, b.Source = clip(m.Title), clip(m.Source)
		}
		books = append(books, b)
	}
	sort.Slice(books, func(i, j int) bool { return books[i].Modified > books[j].Modified })
	size := 0
	for i, b := range books {
		line, err := json.Marshal(b)
		if err != nil {
			return nil, 0, err
		}
		if size += len(line) + 1; size > maxList {
			return books[:i], len(books) - i, nil
		}
	}
	return books, 0, nil
}

func clip(s string) string {
	if r := []rune(s); len(r) > maxText {
		return string(r[:maxText])
	}
	return s
}

// read returns one message: a 32-bit length in native byte order, then that
// many bytes, at most 1 KB since a message names one file.
func read(r io.Reader) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.NativeEndian, &n); err != nil {
		return nil, err
	}
	if n > 1024 {
		return nil, fmt.Errorf("message of %d bytes is over 1 KB", n)
	}
	body := make([]byte, n)
	_, err := io.ReadFull(r, body)
	return body, err
}

// write encodes v as one message: its JSON length, then the JSON.
func write(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := binary.Write(w, binary.NativeEndian, uint32(len(b))); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// register writes the manifest Firefox reads to find this host, and returns
// its path.
func register() (string, error) {
	dirs := map[string]string{
		"linux":  ".mozilla/native-messaging-hosts",
		"darwin": "Library/Application Support/Mozilla/NativeMessagingHosts",
	}
	dir, ok := dirs[runtime.GOOS]
	if !ok {
		return "", fmt.Errorf("-install supports Linux and macOS, not %s", runtime.GOOS)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	manifest, err := json.MarshalIndent(map[string]any{
		"name":               hostName,
		"description":        "Lists the ink-drop library and puts books in the folder a sync tool sends",
		"path":               exe,
		"type":               "stdio",
		"allowed_extensions": []string{extensionID},
	}, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, dir, hostName+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(manifest, '\n'), 0o644)
}
