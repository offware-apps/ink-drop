package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/offware-apps/ink-drop/epub"
	"github.com/offware-apps/ink-drop/folder"
	"github.com/offware-apps/ink-drop/internal/stage"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// A route offers Markdown on this machine as books. Path is a file, or a folder
// whose every Markdown file is a book. Book makes each folder holding a file of
// that name one book instead, of the Markdown beside it and, where Rounds is set,
// in the highest numbered folder there. Files makes one book of the files listed,
// relative to Path. Order puts the parts of a book in order, "*" standing for the
// rest. A route is read only: a book is made in the library when it is ticked.
type route struct {
	Path   string   `json:"path"`
	Tab    string   `json:"tab,omitempty"`
	Book   string   `json:"book,omitempty"`
	Rounds string   `json:"rounds,omitempty"`
	Order  []string `json:"order,omitempty"`
	Title  string   `json:"title,omitempty"`
	Files  []string `json:"files,omitempty"`
}

// A route's book is named to the browser by this prefix, the route's place in
// the config file, and its path inside the route.
const routePrefix = "route:"

// defaultTab is the library folder of a route that names none.
const defaultTab = "notes"

// Raw HTML is kept, details blocks being common in Markdown, and the book drops
// anything that could run.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithRendererOptions(html.WithUnsafe()))

var headingLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// plainName reports whether s names one file or folder, nothing above it.
func plainName(s string) bool {
	return s != "" && s == filepath.Base(s) && !strings.HasPrefix(s, ".") && !strings.ContainsAny(s, `/\`)
}

// checkRoutes expands and checks the routes in place. A route as wide as the home
// folder or the whole disk is refused: it would offer every note on the machine.
func checkRoutes(routes []route, home string) error {
	for i := range routes {
		r := &routes[i]
		if rest, ok := strings.CutPrefix(r.Path, "~/"); ok {
			r.Path = filepath.Join(home, rest)
		}
		if !filepath.IsAbs(r.Path) {
			return fmt.Errorf("route %q needs a full path", r.Path)
		}
		if clean := filepath.Clean(r.Path); clean == filepath.Clean(home) || clean == filepath.Dir(clean) {
			return fmt.Errorf("route %q is too wide, name a folder inside it", r.Path)
		}
		if r.Tab == "" {
			r.Tab = defaultTab
		}
		if !plainName(r.Tab) || (r.Book != "" && !plainName(r.Book)) {
			return fmt.Errorf("route %q: a tab or a book is one plain name", r.Path)
		}
		if r.Book != "" && len(r.Files) > 0 {
			return fmt.Errorf("route %q: book and files do not go together", r.Path)
		}
		if _, err := regexp.Compile(r.Rounds); err != nil {
			return fmt.Errorf("route %q: rounds: %w", r.Path, err)
		}
		for j, file := range r.Files {
			if rest, ok := strings.CutPrefix(file, "~/"); ok {
				r.Files[j] = filepath.Join(home, rest)
			} else if !filepath.IsLocal(file) || !strings.HasSuffix(file, ".md") {
				return fmt.Errorf("route %q: %q is not a Markdown file inside it", r.Path, file)
			}
		}
	}
	return nil
}

// piece is one Markdown file of a book, read through a root at dir so neither a
// link nor a name can reach outside it.
type piece struct {
	dir, rel string
}

// read returns the file, refusing one that a link or a name takes out of dir,
// even one swapped in since the walk.
func (p piece) read() ([]byte, error) {
	root, err := os.OpenRoot(p.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.ReadFile(p.rel)
}

// routeItem is one book a route offers, made or not.
type routeItem struct {
	id, tab, title, where, name string
	modified                    int64
	pieces                      []piece
}

// routeItems walks every route afresh. A route whose path is missing, a drive
// not mounted for one, offers nothing.
func (f folders) routeItems() ([]routeItem, error) {
	var items []routeItem
	for i, r := range f.Routes {
		prefix := routePrefix + strconv.Itoa(i) + ":"
		if len(r.Files) > 0 {
			it := routeItem{id: prefix, tab: r.Tab, title: r.Title, where: filepath.Base(r.Path),
				name: bookName(r.Title, r.Path+"\x00"+strings.Join(r.Files, "\x00"))}
			for _, file := range r.Files {
				if filepath.IsAbs(file) {
					it.pieces = append(it.pieces, piece{filepath.Dir(file), filepath.Base(file)})
				} else {
					it.pieces = append(it.pieces, piece{r.Path, filepath.ToSlash(file)})
				}
			}
			if err := it.head(); err != nil {
				return nil, err
			}
			items = append(items, it)
			continue
		}
		info, err := os.Stat(r.Path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			base := filepath.Base(r.Path)
			it := routeItem{id: prefix + base, tab: r.Tab, title: r.Title, where: filepath.Base(filepath.Dir(r.Path)),
				name: bookName(strings.TrimSuffix(base, ".md"), r.Path), pieces: []piece{{filepath.Dir(r.Path), base}}}
			if err := it.head(); err != nil {
				return nil, err
			}
			items = append(items, it)
			continue
		}
		found, err := markdownFiles(r.Path)
		if err != nil {
			return nil, err
		}
		var rounds *regexp.Regexp
		if r.Rounds != "" {
			rounds = regexp.MustCompile(r.Rounds)
		}
		for _, rel := range found {
			if r.Book != "" && path.Base(rel) != r.Book {
				continue
			}
			it := routeItem{id: prefix + rel, tab: r.Tab, where: strings.TrimPrefix(path.Dir(rel), ".")}
			if r.Book == "" {
				it.name = bookName(strings.TrimSuffix(rel, ".md"), r.Path+"\x00"+rel)
				it.pieces = []piece{{r.Path, rel}}
			} else {
				label := path.Dir(rel)
				if label == "." {
					label = filepath.Base(r.Path)
				}
				it.name = bookName(label, r.Path+"\x00"+rel)
				for _, file := range bookFiles(found, rel, rounds, r.Order) {
					it.pieces = append(it.pieces, piece{r.Path, file})
				}
			}
			if err := it.head(); err != nil {
				return nil, err
			}
			items = append(items, it)
		}
	}
	return items, nil
}

// head fills the title from the first file's first heading, unless the route gave
// one, and the time it changed.
func (it *routeItem) head() error {
	first := it.pieces[0]
	root, err := os.OpenRoot(first.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.Open(first.rel)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	it.modified = info.ModTime().UnixMilli()
	start, err := io.ReadAll(io.LimitReader(file, 4096))
	if err != nil {
		return err
	}
	if it.title == "" {
		it.title = heading(string(start))
	}
	if it.title == "" {
		it.title = strings.TrimSuffix(path.Base(first.rel), ".md")
	}
	return nil
}

// markdownFiles lists the Markdown under dir, as slash paths from it. A hidden
// folder or another git checkout is not this route's, and a link is not followed.
func markdownFiles(dir string) ([]string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var files []string
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == "." {
				return nil
			}
			if _, err := root.Lstat(path.Join(p, ".git")); strings.HasPrefix(d.Name(), ".") || err == nil {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && strings.HasSuffix(p, ".md") {
			files = append(files, p)
		}
		return nil
	})
	return files, err
}

// bookFiles returns the files of the book found at main, in reading order: the
// Markdown beside it and in its highest numbered round.
func bookFiles(files []string, main string, rounds *regexp.Regexp, order []string) []string {
	dir := ""
	if i := strings.LastIndex(main, "/"); i >= 0 {
		dir = main[:i+1]
	}
	latest, last := -1, ""
	for _, f := range files {
		rest, ok := strings.CutPrefix(f, dir)
		first, _, nested := strings.Cut(rest, "/")
		if rounds == nil || !ok || !nested {
			continue
		}
		if m := rounds.FindStringSubmatch(first); len(m) > 1 {
			if n, err := strconv.Atoi(m[1]); err == nil && n > latest {
				latest, last = n, first+"/"
			}
		}
	}
	var in []string
	for _, f := range files {
		rest, ok := strings.CutPrefix(f, dir)
		if !ok {
			continue
		}
		inRound := last != "" && strings.HasPrefix(rest, last) && !strings.Contains(rest[len(last):], "/")
		if inRound || !strings.Contains(rest, "/") {
			in = append(in, f)
		}
	}
	sort.Slice(in, func(i, j int) bool {
		if in[i] == main || in[j] == main {
			return in[i] == main
		}
		a, b := fileRank(in[i], order), fileRank(in[j], order)
		if a != b {
			return a < b
		}
		return in[i] < in[j]
	})
	return in
}

// fileRank places a file by the first order entry its name starts with, any other
// where "*" stands, or after them all.
func fileRank(file string, order []string) int {
	name := path.Base(file)
	other := len(order)
	for i, start := range order {
		if start == "*" {
			other = i
		} else if strings.HasPrefix(name, start) {
			return i
		}
	}
	return other
}

// heading returns the first heading in plain words, its links reduced to their text.
func heading(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if rest, ok := strings.CutPrefix(line, "# "); ok {
			return strings.TrimSpace(headingLink.ReplaceAllString(rest, "$1"))
		}
	}
	return ""
}

// bookName names a route's book from where it is, so the name stays the same
// across runs, and adds a hash of that place: two places a slug folds together,
// a-b/x and a/b-x for one, still get two names.
func bookName(label, place string) string {
	sum := sha256.Sum256([]byte(place))
	return stage.Slug(label) + "-" + hex.EncodeToString(sum[:3]) + ".epub"
}

// findRoute returns the book a route offers under id. Only an id this walk found
// is accepted, so a name from the browser is never joined to a path.
func (f folders) findRoute(id string) (routeItem, error) {
	items, err := f.routeItems()
	if err != nil {
		return routeItem{}, err
	}
	for _, it := range items {
		if it.id == id {
			return it, nil
		}
	}
	return routeItem{}, fmt.Errorf("%q is not in your routes", id)
}

// sendRoute makes the book afresh in the library, then sends it as any other.
func (f folders) sendRoute(id, to string) error {
	it, err := f.findRoute(id)
	if err != nil {
		return err
	}
	if _, err := f.transmitter(to); err != nil {
		return err
	}
	var list, parts strings.Builder
	for i, p := range it.pieces {
		text, err := p.read()
		if err != nil {
			return err
		}
		part := strings.TrimSuffix(path.Base(p.rel), ".md")
		fmt.Fprintf(&list, `<li><a href="#part-%d">%s</a></li>`, i, part)
		if len(it.pieces) > 1 {
			fmt.Fprintf(&parts, `<h1 id="part-%d">%s</h1>`, i, part)
		}
		var out bytes.Buffer
		if err := markdown.Convert(text, &out); err != nil {
			return err
		}
		parts.Write(out.Bytes())
	}
	doc := "<!doctype html><html><body>"
	if len(it.pieces) > 1 {
		doc += "<ul>" + list.String() + "</ul>"
	}
	doc += parts.String() + "</body></html>"
	now := time.Now().UTC()
	book, err := epub.Build(epub.Book{Title: it.title, Modified: now}, strings.NewReader(doc))
	if err != nil {
		return err
	}
	if _, err := folder.Put(filepath.Join(f.Library, it.tab), it.name, book, folder.Meta{Title: it.title, Fetched: now, Stage: "ink-send"}); err != nil {
		return err
	}
	return f.send(it.tab+"/"+it.name, to)
}

// unsendRoute takes the book back, and out of the library once no transmitter
// holds it: only a ticked book is kept made.
func (f folders) unsendRoute(id, to string) error {
	it, err := f.findRoute(id)
	if err != nil {
		return err
	}
	name := it.tab + "/" + it.name
	if err := f.unsend(name, to); err != nil {
		return err
	}
	for _, t := range f.Transmitters {
		if _, err := os.Lstat(filepath.Join(t.Folder, name)); err == nil {
			return nil
		}
	}
	err = f.remove(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
