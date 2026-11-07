// Command ink-send is a browser native-messaging host: it lists the library
// for the ink-drop extension and puts a book in, or takes it out of, the folder
// of a transmitter, which is anything that sends on what lands there. It touches
// only the folders the user wrote in its config file, and the browser names a
// transmitter and never a path. It holds no code that starts a program or opens a
// connection, so neither a setting nor a message can make it do either.
// `ink-send -install` registers it with Firefox for the current user.
package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

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
)

// A transmitter is a folder something sends on from: a sync tool, a mounted drive.
type transmitter struct {
	Name   string `json:"name"`
	Folder string `json:"folder"`
}

// folders is the config file. Downloads is optional: where the browser saves
// pages, moved into Library since an extension can save nowhere else.
type folders struct {
	Library      string        `json:"library"`
	Downloads    string        `json:"downloads,omitempty"`
	Transmitters []transmitter `json:"transmitters,omitempty"`
	Routes       []route       `json:"routes,omitempty"`
}

// defaults is the config file -install writes where none exists.
const defaults = `{
  "library": "~/Books",
  "downloads": "~/Downloads/ink-drop",
  "transmitters": []
}
`

type request struct {
	Action string `json:"action"`
	File   string `json:"file"`
	To     string `json:"to"`
}

type book struct {
	Name     string `json:"name"`
	Title    string `json:"title,omitempty"`
	Source   string `json:"source,omitempty"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
	// Sent names each transmitter whose folder holds the book.
	Sent []string `json:"sent,omitempty"`
	// Folder is the library folder holding it, Where the folder of a route's book
	// inside its route, and Route says the name is a route's id.
	Folder string `json:"folder,omitempty"`
	Where  string `json:"where,omitempty"`
	Route  bool   `json:"route,omitempty"`
}

type reply struct {
	OK           bool              `json:"ok"`
	Error        string            `json:"error,omitempty"`
	Books        []book            `json:"books,omitempty"`
	More         int               `json:"more,omitempty"`
	Moved        map[string]string `json:"moved,omitempty"`
	Transmitters []string          `json:"transmitters,omitempty"`
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
		fmt.Printf("ink-send: registered in %s\nink-send: its folders and transmitters are in %s\n", path, configFile)
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
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return f, err
	}
	expand := func(p *string) {
		if rest, ok := strings.CutPrefix(*p, "~/"); ok {
			*p = filepath.Join(home, rest)
		}
	}
	expand(&f.Library)
	expand(&f.Downloads)
	for i := range f.Transmitters {
		t := &f.Transmitters[i]
		expand(&t.Folder)
		if t.Name == "" || !filepath.IsAbs(t.Folder) {
			return f, fmt.Errorf("%s: a transmitter needs a name and a full path", path)
		}
	}
	if !filepath.IsAbs(f.Library) || (f.Downloads != "" && !filepath.IsAbs(f.Downloads)) {
		return f, fmt.Errorf("%s: library and downloads need full paths", path)
	}
	if err := checkRoutes(f.Routes, home); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
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
		moved, err := f.collect()
		if err != nil {
			return reply{}, err
		}
		books, more, err := f.list()
		names := make([]string, len(f.Transmitters))
		for i, t := range f.Transmitters {
			names[i] = t.Name
		}
		return reply{Books: books, More: more, Moved: moved, Transmitters: names}, err
	case "send":
		if strings.HasPrefix(req.File, routePrefix) {
			return reply{}, f.sendRoute(req.File, req.To)
		}
		return reply{}, f.send(req.File, req.To)
	case "unsend":
		if strings.HasPrefix(req.File, routePrefix) {
			return reply{}, f.unsendRoute(req.File, req.To)
		}
		return reply{}, f.unsend(req.File, req.To)
	case "delete":
		return reply{}, f.remove(req.File)
	}
	return reply{}, fmt.Errorf("unknown action %q", req.Action)
}

// transmitter returns the one called name, or the first where name is empty.
func (f folders) transmitter(name string) (transmitter, error) {
	for _, t := range f.Transmitters {
		if t.Name == name || name == "" {
			return t, nil
		}
	}
	if name == "" {
		return transmitter{}, errors.New("no transmitter in the config file")
	}
	return transmitter{}, fmt.Errorf("no transmitter called %q", name)
}

// isBook reports whether name is a file a reader opens, alone or in one folder
// of the library, so a name from the browser cannot point outside the folder it
// is joined to.
func isBook(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	parts := strings.Split(name, "/")
	if !filepath.IsLocal(name) || len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return ext == ".epub" || ext == ".pdf"
}

// inLibrary returns the path of the book called name, refusing a link that
// would reach a file outside the library.
func (f folders) inLibrary(name string) (string, error) {
	if !isBook(name) {
		return "", fmt.Errorf("%q is not a book name", name)
	}
	path := filepath.Join(f.Library, name)
	if dir := filepath.Dir(name); dir != "." {
		info, err := os.Lstat(filepath.Join(f.Library, dir))
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%q is not a folder", dir)
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", name)
	}
	return path, nil
}

// send hard-links the book into the transmitter's folder: no second copy on
// disk, and a sync tool never sees it half written. A folder on another drive
// takes a copy. A file of that name made from an older book is replaced.
func (f folders) send(name, to string) error {
	path, err := f.inLibrary(name)
	if err != nil {
		return err
	}
	t, err := f.transmitter(to)
	if err != nil {
		return err
	}
	dest := filepath.Join(t.Folder, name)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	book, err := os.Stat(path)
	if err != nil {
		return err
	}
	if there, err := os.Lstat(dest); err == nil {
		if os.SameFile(book, there) {
			return nil
		}
		if err := os.Remove(dest); err != nil {
			return err
		}
	}
	if os.Link(path, dest) == nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return folder.WriteAtomic(dest, data)
}

func (f folders) unsend(name, to string) error {
	if !isBook(name) {
		return fmt.Errorf("%q is not a book name", name)
	}
	t, err := f.transmitter(to)
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(t.Folder, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// remove deletes the book, its link in every transmitter's folder and its sidecar.
func (f folders) remove(name string) error {
	path, err := f.inLibrary(name)
	if err != nil {
		return err
	}
	for _, t := range f.Transmitters {
		if err := f.unsend(name, t.Name); err != nil {
			return err
		}
	}
	err = os.Remove(filepath.Join(filepath.Dir(path), folder.SidecarDir, filepath.Base(path)+".json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Remove(path)
}

// collect moves finished downloads into the library, and returns the new name
// of each one a book already there forced to change. A browser writes an empty
// file first, so an empty one is still downloading.
func (f folders) collect() (map[string]string, error) {
	if f.Downloads == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(f.Downloads)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var moved map[string]string
	for _, e := range entries {
		name := e.Name()
		info, err := e.Info()
		if err != nil || !isBook(name) || !info.Mode().IsRegular() || info.Size() == 0 {
			continue
		}
		to := free(f.Library, name)
		if err := os.Rename(filepath.Join(f.Downloads, name), filepath.Join(f.Library, to)); err != nil {
			return moved, err
		}
		if to != name {
			if moved == nil {
				moved = map[string]string{}
			}
			moved[name] = to
		}
	}
	return moved, nil
}

// free returns name, or name with a number added if dir already holds it.
func free(dir, name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i, try := 1, name; ; i++ {
		if _, err := os.Lstat(filepath.Join(dir, try)); errors.Is(err, fs.ErrNotExist) {
			return try
		}
		try = fmt.Sprintf("%s-%d%s", stem, i, ext)
	}
}

// names returns every book's name in dir, and in dir's folders, one level down.
func names(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() {
			found = append(found, e.Name())
			continue
		}
		inside, err := os.ReadDir(filepath.Join(dir, e.Name()))
		if err != nil || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		for _, sub := range inside {
			found = append(found, e.Name()+"/"+sub.Name())
		}
	}
	return found, nil
}

// list returns the library's newest books, and how many more it left out. A
// title and a source come from the sidecar, where the book has one.
func (f folders) list() ([]book, int, error) {
	found, err := names(f.Library)
	if err != nil {
		return nil, 0, err
	}
	routed, err := f.routeItems()
	if err != nil {
		return nil, 0, err
	}
	books := []book{}
	made := map[string]bool{}
	for _, it := range routed {
		name := it.tab + "/" + it.name
		made[name] = true
		b := book{Name: it.id, Title: clip(it.title), Modified: it.modified, Folder: it.tab, Where: clip(it.where), Route: true}
		if info, err := os.Lstat(filepath.Join(f.Library, name)); err == nil {
			b.Size = info.Size()
		}
		for _, t := range f.Transmitters {
			if _, err := os.Lstat(filepath.Join(t.Folder, name)); err == nil {
				b.Sent = append(b.Sent, t.Name)
			}
		}
		books = append(books, b)
	}
	for _, name := range found {
		if made[name] {
			continue
		}
		path := filepath.Join(f.Library, name)
		info, err := os.Lstat(path)
		if err != nil || !isBook(name) || !info.Mode().IsRegular() {
			continue
		}
		b := book{Name: name, Size: info.Size(), Modified: info.ModTime().UnixMilli()}
		if dir, _, ok := strings.Cut(name, "/"); ok {
			b.Folder = dir
		}
		if m, err := folder.Get(path); err == nil {
			b.Title, b.Source = clip(m.Title), clip(m.Source)
		}
		for _, t := range f.Transmitters {
			if _, err := os.Lstat(filepath.Join(t.Folder, name)); err == nil {
				b.Sent = append(b.Sent, t.Name)
			}
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
		"description":        "Lists the ink-drop library and moves books to the folders that send them on",
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
