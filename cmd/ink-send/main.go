// Command ink-send is a browser native-messaging host: it lists the library
// for the ink-drop extension and puts a book in, or takes it out of, a folder
// any sync tool can send on. It touches only the folders the user wrote in its
// config file. `ink-send -install` registers it with Firefox for the current
// user.
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

// folders is the config file. Downloads is optional: where the browser saves
// pages, moved into Library since an extension can save nowhere else.
type folders struct {
	Library   string `json:"library"`
	Outbox    string `json:"outbox"`
	Downloads string `json:"downloads,omitempty"`
}

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
	Sent     bool   `json:"sent"`
}

type reply struct {
	OK    bool              `json:"ok"`
	Error string            `json:"error,omitempty"`
	Books []book            `json:"books,omitempty"`
	More  int               `json:"more,omitempty"`
	Moved map[string]string `json:"moved,omitempty"`
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
		fmt.Printf("ink-send: registered in %s\nink-send: the folders it uses are read from %s\n", path, configFile)
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
		return f, fmt.Errorf(`write {"library": "~/Books", "outbox": "~/Books/to-tablet"} into %s`, path)
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
	for _, p := range []*string{&f.Library, &f.Outbox, &f.Downloads} {
		if rest, ok := strings.CutPrefix(*p, "~/"); ok {
			*p = filepath.Join(home, rest)
		}
	}
	if !filepath.IsAbs(f.Library) || !filepath.IsAbs(f.Outbox) || (f.Downloads != "" && !filepath.IsAbs(f.Downloads)) {
		return f, fmt.Errorf("%s: library and outbox need full paths", path)
	}
	return f, nil
}

func (f folders) handle(req request) (reply, error) {
	switch req.Action {
	case "list":
		moved, err := f.collect()
		if err != nil {
			return reply{}, err
		}
		books, more, err := f.list()
		return reply{Books: books, More: more, Moved: moved}, err
	case "send":
		return reply{}, f.send(req.File)
	case "unsend":
		return reply{}, f.unsend(req.File)
	case "delete":
		return reply{}, f.remove(req.File)
	}
	return reply{}, fmt.Errorf("unknown action %q", req.Action)
}

// isBook reports whether name is a plain file name a reader opens, so a name
// from the browser cannot point outside the folder it is joined to.
func isBook(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return name == filepath.Base(name) && !strings.HasPrefix(name, ".") && (ext == ".epub" || ext == ".pdf")
}

// inLibrary returns the path of the book called name, refusing a link that
// would reach a file outside the library.
func (f folders) inLibrary(name string) (string, error) {
	if !isBook(name) {
		return "", fmt.Errorf("%q is not a book name", name)
	}
	path := filepath.Join(f.Library, name)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", name)
	}
	return path, nil
}

// send hard-links the book into the outbox: no second copy on disk, and a sync
// tool never sees it half written. Both folders must be on one drive.
func (f folders) send(name string) error {
	path, err := f.inLibrary(name)
	if err != nil {
		return err
	}
	err = os.Link(path, filepath.Join(f.Outbox, name))
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	return err
}

func (f folders) unsend(name string) error {
	if !isBook(name) {
		return fmt.Errorf("%q is not a book name", name)
	}
	err := os.Remove(filepath.Join(f.Outbox, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// remove deletes the book, its link in the outbox and its sidecar.
func (f folders) remove(name string) error {
	path, err := f.inLibrary(name)
	if err != nil {
		return err
	}
	if err := f.unsend(name); err != nil {
		return err
	}
	err = os.Remove(filepath.Join(f.Library, folder.SidecarDir, name+".json"))
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

// list returns the library's newest books, and how many more it left out. A
// title and a source come from the sidecar, where the book has one.
func (f folders) list() ([]book, int, error) {
	entries, err := os.ReadDir(f.Library)
	if err != nil {
		return nil, 0, err
	}
	books := []book{}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !isBook(e.Name()) || !info.Mode().IsRegular() {
			continue
		}
		b := book{Name: e.Name(), Size: info.Size(), Modified: info.ModTime().UnixMilli()}
		if m, err := folder.Get(filepath.Join(f.Library, e.Name())); err == nil {
			b.Title, b.Source = clip(m.Title), clip(m.Source)
		}
		if _, err := os.Lstat(filepath.Join(f.Outbox, e.Name())); err == nil {
			b.Sent = true
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
		"description":        "Lists the ink-drop library and moves books to the folder that syncs on",
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
