package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/offware-apps/ink-drop/epub"
	"github.com/offware-apps/ink-drop/folder"
	"github.com/offware-apps/ink-drop/internal/stage"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

// A route offers Markdown on this machine as books. Path is a file, or a folder
// whose every Markdown file is a book. Book makes each folder holding a file of
// that name one book instead, of the Markdown beside it and, where Rounds is set,
// in the highest numbered folder there. Files makes one book of the Markdown files
// listed, each inside Path. Order puts the parts of a book in order, "*" standing
// for the rest. A route is read only: a book is made in the library when it is
// ticked.
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

// maxMarkdown bounds the Markdown of one book a route offers; bounded bounds the
// time and memory of making it.
const maxMarkdown = 1 << 20

// Raw HTML is kept, details blocks being common in Markdown, and the book drops
// anything that could run.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithRendererOptions(gmhtml.WithUnsafe()))

var headingLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// plainName reports whether s names one file or folder, nothing above it.
func plainName(s string) bool {
	return s == filepath.Base(s) && filepath.IsLocal(s) && !strings.HasPrefix(s, ".")
}

// hidden reports whether any element of p starts with a dot.
func hidden(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
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
		if inside(home, r.Path) {
			return fmt.Errorf("route %q is too wide, name a folder inside it", r.Path)
		}
		if r.Tab == "" {
			r.Tab = defaultTab
		}
		if !plainName(r.Tab) || (r.Book != "" && !plainName(r.Book)) {
			return fmt.Errorf("route %q: a tab or a book is one plain name", r.Path)
		}
		if hidden(r.Path) || hidden(resolve(r.Path)) {
			return fmt.Errorf("route %q is hidden or inside a hidden folder", r.Path)
		}
		if r.Book != "" && len(r.Files) > 0 {
			return fmt.Errorf("route %q: book and files do not go together", r.Path)
		}
		if _, err := regexp.Compile(r.Rounds); err != nil {
			return fmt.Errorf("route %q: rounds: %w", r.Path, err)
		}
		for _, file := range r.Files {
			if !filepath.IsLocal(file) || hidden(file) || strings.HasPrefix(file, "~") || !strings.HasSuffix(file, ".md") {
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

// read returns at most max bytes of the file, through a root at dir.
func (p piece) read(max int64) ([]byte, error) {
	root, err := openDir(p.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, _, err := readPlain(root, p.rel, max)
	return data, err
}

// routeItem is one book a route offers, made or not.
type routeItem struct {
	id, tab, title, where, name string
	modified                    int64
	pieces                      []piece
}

// readable fills the book's title and time from its first file and reports
// whether every file of it can be read, at most maxMarkdown in all, so a book is
// offered only when all of it can be made.
func (it *routeItem) readable() bool {
	var total int64
	for i, p := range it.pieces {
		root, err := openDir(p.dir)
		if err != nil {
			return false
		}
		file, info, err := openPlain(root, p.rel)
		root.Close()
		if err != nil {
			return false
		}
		var start []byte
		if i == 0 {
			start, err = io.ReadAll(io.LimitReader(file, 4096))
		}
		file.Close()
		if total += info.Size(); err != nil || total > maxMarkdown {
			return false
		}
		if i > 0 {
			continue
		}
		it.modified = info.ModTime().UnixMilli()
		if it.title == "" {
			it.title = heading(string(start))
		}
		if it.title == "" {
			it.title = strings.TrimSuffix(path.Base(p.rel), ".md")
		}
	}
	return true
}

// routeItems walks every route afresh. What it cannot read, a route whose path is
// missing because a drive is not mounted, a folder or file it may not open or a
// link, it leaves out, so one bad file never hides the rest.
func (f folders) routeItems() []routeItem {
	var items []routeItem
	for i, r := range f.Routes {
		prefix := routePrefix + strconv.Itoa(i) + ":"
		if len(r.Files) > 0 {
			it := routeItem{id: prefix, tab: r.Tab, title: r.Title, where: filepath.Base(r.Path),
				name: bookName(r.Title, r.Path+"\x00"+strings.Join(r.Files, "\x00"))}
			for _, file := range r.Files {
				it.pieces = append(it.pieces, piece{r.Path, file})
			}
			if it.readable() {
				items = append(items, it)
			}
			continue
		}
		info, err := os.Stat(r.Path)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			base := filepath.Base(r.Path)
			if !strings.HasSuffix(base, ".md") {
				continue
			}
			it := routeItem{id: prefix + base, tab: r.Tab, title: r.Title, where: filepath.Base(filepath.Dir(r.Path)),
				name: bookName(strings.TrimSuffix(base, ".md"), r.Path), pieces: []piece{{filepath.Dir(r.Path), base}}}
			if it.readable() {
				items = append(items, it)
			}
			continue
		}
		found, err := markdownFiles(r.Path)
		if err != nil {
			continue
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
			if it.readable() {
				items = append(items, it)
			}
		}
	}
	return items
}

// markdownFiles lists the Markdown under dir, as slash paths from it. A hidden
// folder or another git checkout is not this route's, and each folder is opened
// only as a folder, never through a link; readPlain refuses what is not a file.
func markdownFiles(dir string) ([]string, error) {
	root, err := openDir(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var files []string
	walkMarkdown(root, "", 0, &files)
	return files, nil
}

// maxDepth bounds how many folders deep a walk goes, since each level costs a
// look at every folder above it.
const maxDepth = 16

func walkMarkdown(root *os.Root, prefix string, depth int, files *[]string) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !e.IsDir() {
			if strings.HasSuffix(name, ".md") {
				*files = append(*files, prefix+name)
			}
			continue
		}
		sub, err := root.OpenRoot(name)
		if err != nil {
			continue
		}
		if _, err := sub.Lstat(".git"); err != nil {
			if depth < maxDepth {
				walkMarkdown(sub, prefix+name+"/", depth+1, files)
			}
		}
		sub.Close()
	}
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
	for _, it := range f.routeItems() {
		if it.id == id {
			return it, nil
		}
	}
	return routeItem{}, fmt.Errorf("%q is not in your routes", id)
}

// sendRoute makes the book afresh in the library, then sends it as any other.
func (f folders) sendRoute(id string) error {
	it, err := f.findRoute(id)
	if err != nil {
		return err
	}
	// A book is made only for a sync folder that is there to take it.
	out, err := f.openSync()
	if err != nil {
		return err
	}
	if out == nil {
		return errNoSync
	}
	out.Close()
	budget := int64(maxMarkdown)
	texts := make([][]byte, len(it.pieces))
	for i, p := range it.pieces {
		// The cap holds for the whole book, should a file grow between the check
		// and the read.
		text, err := p.read(budget)
		if err != nil {
			return err
		}
		budget -= int64(len(text))
		texts[i] = text
	}
	now := time.Now().UTC()
	book, err := bounded(func() ([]byte, error) { return it.build(texts, now) })
	if err != nil {
		return err
	}
	if err := f.put(it.tab, it.name, book, folder.Meta{Title: it.title, Fetched: now, Stage: "ink-send"}); err != nil {
		return err
	}
	return f.send(it.tab + "/" + it.name)
}

// build turns the book's Markdown into an EPUB, a list of its parts first where
// there are several.
func (it routeItem) build(texts [][]byte, now time.Time) ([]byte, error) {
	var list, parts strings.Builder
	for i, p := range it.pieces {
		part := html.EscapeString(strings.TrimSuffix(path.Base(p.rel), ".md"))
		fmt.Fprintf(&list, `<li><a href="#part-%d">%s</a></li>`, i, part)
		if len(it.pieces) > 1 {
			fmt.Fprintf(&parts, `<h1 id="part-%d">%s</h1>`, i, part)
		}
		var out bytes.Buffer
		if err := markdown.Convert(texts[i], &out); err != nil {
			return nil, err
		}
		parts.Write(out.Bytes())
	}
	doc := "<!doctype html><html><body>"
	if len(it.pieces) > 1 {
		doc += "<ul>" + list.String() + "</ul>"
	}
	doc += parts.String() + "</body></html>"
	return epub.Build(epub.Book{Title: it.title, Modified: now}, strings.NewReader(doc))
}

// Some Markdown takes time or memory that grows with the square of its size, a
// table of many columns for one, so making a book is given at most these.
var (
	maxBuild = 20 * time.Second
	maxHeap  = uint64(512 << 20)
)

// errTooHeavy is what a tick gets for a note that would take more than that.
var errTooHeavy = errors.New("this note takes too much time or memory to make into a book")

// bounded runs work in its own goroutine and gives up once it passes maxBuild or
// the heap passes maxHeap. ink-send is one process per message, so the work it
// gives up on ends when the process does, right after the answer.
func bounded(work func() ([]byte, error)) ([]byte, error) {
	type result struct {
		book []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		book, err := work()
		done <- result{book, err}
	}()
	deadline := time.After(maxBuild)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case r := <-done:
			return r.book, r.err
		case <-deadline:
			return nil, errTooHeavy
		case <-tick.C:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > maxHeap {
				return nil, errTooHeavy
			}
		}
	}
}

// put writes a book and its sidecar into a folder of the library, sidecar first
// as the folder contract asks, through a root no link inside the library leaves.
func (f folders) put(tab, name string, data []byte, m folder.Meta) error {
	lib, err := openDir(f.Library)
	if err != nil {
		return err
	}
	defer lib.Close()
	m.Schema, m.File = folder.Schema, name
	side, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := writeIn(lib, tab+"/"+folder.SidecarDir+"/"+name+".json", bytes.NewReader(append(side, '\n'))); err != nil {
		return err
	}
	return writeIn(lib, tab+"/"+name, bytes.NewReader(data))
}

// unsendRoute takes the book out of the sync folder and the library: only a
// ticked book is kept made.
func (f folders) unsendRoute(id string) error {
	it, err := f.findRoute(id)
	if err != nil {
		return err
	}
	// The name comes from the route's own walk, so its copy in the sync folder is
	// taken back even when the library's copy is already gone.
	name := it.tab + "/" + it.name
	if err := f.unsend(name); err != nil {
		return err
	}
	err = f.remove(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
