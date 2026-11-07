package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// withRoutes is setup with routes checked as load checks them.
func withRoutes(t *testing.T, routes ...route) folders {
	t.Helper()
	f := setup(t)
	f.Routes = routes
	if err := checkRoutes(f.Routes, "/home/reader"); err != nil {
		t.Fatal(err)
	}
	return f
}

func listed(t *testing.T, f folders) map[string]book {
	t.Helper()
	books, _, err := f.list()
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]book{}
	for _, b := range books {
		m[b.Name] = b
	}
	return m
}

func bookText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range z.File {
		if strings.HasSuffix(f.Name, "text.xhtml") {
			r, _ := f.Open()
			x, _ := io.ReadAll(r)
			return string(x)
		}
	}
	t.Fatal("no text in the book")
	return ""
}

// snapshot records every file under dir with its size and time, to show a route
// is left as it was.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		info, _ := d.Info()
		b.WriteString(p + " " + info.Mode().String() + " " + info.ModTime().String() + "\n")
		return nil
	})
	return b.String()
}

func TestRouteKinds(t *testing.T) {
	notes := t.TempDir()
	put(t, filepath.Join(notes, "idea.md"), "# An idea\n")
	os.MkdirAll(filepath.Join(notes, "recipes", "italian"), 0o755)
	put(t, filepath.Join(notes, "recipes", "soup.md"), "# Soup\n")
	put(t, filepath.Join(notes, "recipes", "italian", "pasta.md"), "no heading\n")
	put(t, filepath.Join(notes, "recipes", "photo.png"), "p")
	os.MkdirAll(filepath.Join(notes, "guide"), 0o755)
	put(t, filepath.Join(notes, "guide", "intro.md"), "# Intro\n")
	put(t, filepath.Join(notes, "guide", "setup.md"), "# Setup\n")
	f := withRoutes(t,
		route{Path: filepath.Join(notes, "idea.md")},
		route{Path: filepath.Join(notes, "recipes"), Tab: "food"},
		route{Path: filepath.Join(notes, "guide"), Title: "My guide", Files: []string{"setup.md", "intro.md"}},
	)
	got := listed(t, f)
	want := map[string]string{
		"route:0:idea.md":          "An idea",
		"route:1:soup.md":          "Soup",
		"route:1:italian/pasta.md": "pasta",
		"route:2:":                 "My guide",
	}
	if len(got) != len(want) {
		t.Errorf("listed %v", got)
	}
	for id, title := range want {
		b, ok := got[id]
		if !ok || b.Title != title || !b.Route || b.Sent {
			t.Errorf("%s: %+v", id, b)
		}
	}
	if got["route:1:soup.md"].Folder != "food" || got["route:0:idea.md"].Folder != defaultTab {
		t.Errorf("tabs: %+v", got)
	}

	// A listed file order is the book's order.
	if err := f.send("route:2:"); err == nil {
		t.Error("a route's id went through the plain send")
	}
	if err := f.sendRoute("route:2:"); err != nil {
		t.Fatal(err)
	}
	it, _ := f.findRoute("route:2:")
	text := bookText(t, filepath.Join(f.Sync, it.tab, it.name))
	if strings.Index(text, "Setup") > strings.Index(text, "Intro") || !strings.Contains(text, `href="#part-1"`) {
		t.Error("the files are not in the order listed")
	}
}

func TestBookRoute(t *testing.T) {
	reviews := t.TempDir()
	dir := filepath.Join(reviews, "5382-sponsor")
	os.MkdirAll(filepath.Join(dir, "1-aaa"), 0o755)
	os.MkdirAll(filepath.Join(dir, "2-bbb", "tests"), 0o755)
	put(t, filepath.Join(dir, "overview.md"), "# Sponsorship\n\n<details><summary>more</summary>\n\nhidden_word\n\n</details>\n")
	put(t, filepath.Join(dir, "1-aaa", "comment.md"), "old_word\n")
	put(t, filepath.Join(dir, "2-bbb", "comment.md"), "new_word\n")
	put(t, filepath.Join(dir, "2-bbb", "claims.md"), "| a |\n| --- |\n| claim_word |\n")
	put(t, filepath.Join(dir, "2-bbb", "tests", "case.md"), "test_word\n")
	f := withRoutes(t, route{Path: reviews, Tab: "reviews", Book: "overview.md", Rounds: `^(\d+)-`, Order: []string{"overview", "comment", "*"}})
	got := listed(t, f)
	b, ok := got["route:0:5382-sponsor/overview.md"]
	if len(got) != 1 || !ok || b.Title != "Sponsorship" || b.Where != "5382-sponsor" {
		t.Fatalf("listed %v", got)
	}
	before := snapshot(t, reviews)
	if err := f.sendRoute(b.Name); err != nil {
		t.Fatal(err)
	}
	it, _ := f.findRoute(b.Name)
	sent := filepath.Join(f.Sync, "reviews", it.name)
	text := bookText(t, sent)
	for _, want := range []string{"new_word", "claim_word", "hidden_word", "<details", "<table"} {
		if !strings.Contains(text, want) {
			t.Errorf("%s is missing from the book", want)
		}
	}
	for _, gone := range []string{"old_word", "test_word"} {
		if strings.Contains(text, gone) {
			t.Errorf("%s is in the book", gone)
		}
	}
	if strings.Index(text, "new_word") > strings.Index(text, "claim_word") {
		t.Error("the comment comes after the claims")
	}
	if got := listed(t, f)[b.Name]; !got.Sent || got.Size == 0 {
		t.Errorf("after the tick: %+v", got)
	}
	if listed(t, f)["reviews/"+it.name].Name != "" {
		t.Error("the made book is listed twice")
	}

	// Unticking takes it from the sync folder and the library; the route never
	// changes.
	if err := f.unsendRoute(b.Name); err != nil {
		t.Fatal(err)
	}
	if exists(sent) || exists(filepath.Join(f.Library, "reviews", it.name)) || exists(filepath.Join(f.Library, "reviews", ".ink", it.name+".json")) {
		t.Error("an unticked book is still made")
	}
	if after := snapshot(t, reviews); after != before {
		t.Errorf("the route changed:\n%s\n%s", before, after)
	}
}

// Guard 1: only an id the walk found is accepted, so a name from the browser is
// never a path.
func TestOnlyListedIDs(t *testing.T) {
	root := t.TempDir()
	notes := filepath.Join(root, "notes")
	os.MkdirAll(filepath.Join(notes, ".hidden"), 0o755)
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	put(t, filepath.Join(notes, ".hidden", "h.md"), "# Hidden\n")
	put(t, filepath.Join(root, "secret.md"), "# Secret\n")
	f := withRoutes(t, route{Path: notes})
	for _, id := range []string{
		"route:0:../secret.md", "route:0:/etc/passwd", "route:0:.hidden/h.md", "route:0:", "route:1:a.md",
		"route:-1:a.md", "route:0:a.md/", "route:0:./a.md", "route:00:a.md", "route:0:b.md",
	} {
		if err := f.sendRoute(id); err == nil {
			t.Errorf("%q was accepted", id)
		}
		if err := f.unsendRoute(id); err == nil {
			t.Errorf("unsend %q was accepted", id)
		}
	}
	if entries, _ := os.ReadDir(f.Sync); len(entries) != 0 {
		t.Errorf("the sync folder holds %v", entries)
	}
	f.Sync = ""
	if err := f.sendRoute("route:0:a.md"); err == nil {
		t.Error("a book was made with no sync folder to take it")
	}
	if entries, _ := os.ReadDir(f.Library); len(entries) != 0 {
		t.Errorf("a refused tick still made %v", entries)
	}
	if _, err := f.handle(request{Action: "delete", File: "route:0:a.md"}); err == nil || !exists(filepath.Join(notes, "a.md")) {
		t.Error("delete reached a route's file")
	}
}

// Guard 2: a link leads nowhere outside a route, even one swapped in after the
// list, and another checkout inside it is not its.
func TestNoWayOutOfARoute(t *testing.T) {
	root := t.TempDir()
	notes := filepath.Join(root, "notes")
	outside := filepath.Join(root, "outside")
	os.MkdirAll(notes, 0o755)
	os.MkdirAll(outside, 0o755)
	put(t, filepath.Join(outside, "secret.md"), "# Secret\n\nsecret_word\n")
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	os.Symlink(outside, filepath.Join(notes, "linked"))
	os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(notes, "link.md"))
	os.MkdirAll(filepath.Join(notes, "checkout"), 0o755)
	put(t, filepath.Join(notes, "checkout", ".git"), "gitdir: elsewhere")
	put(t, filepath.Join(notes, "checkout", "c.md"), "# Other checkout\n")
	f := withRoutes(t, route{Path: notes})
	got := listed(t, f)
	if len(got) != 1 || got["route:0:a.md"].Name == "" {
		t.Errorf("listed %v", got)
	}
	// The file is replaced by a link between the list and the tick.
	os.Remove(filepath.Join(notes, "a.md"))
	os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(notes, "a.md"))
	f.sendRoute("route:0:a.md")
	filepath.WalkDir(f.Library, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".epub") && strings.Contains(bookText(t, p), "secret_word") {
			t.Errorf("%s holds a file from outside the route", p)
		}
		return nil
	})

	// The same holds for a file a route lists by name.
	g := withRoutes(t, route{Path: notes, Files: []string{"linked/secret.md"}})
	if err := g.sendRoute("route:0:"); err == nil {
		t.Error("a listed file reached outside its route through a link")
	}
}

// The read itself refuses a way out, for a file swapped for a link after the walk.
func TestReadStaysInside(t *testing.T) {
	root := t.TempDir()
	notes := filepath.Join(root, "notes")
	os.MkdirAll(notes, 0o755)
	put(t, filepath.Join(root, "secret.md"), "secret")
	os.Symlink(filepath.Join(root, "secret.md"), filepath.Join(notes, "a.md"))
	os.Symlink(root, filepath.Join(notes, "up"))
	// A link that stays inside the root is refused as well: it could name any
	// file beside the route.
	put(t, filepath.Join(notes, "real.md"), "real")
	os.Symlink("real.md", filepath.Join(notes, "alias.md"))
	for _, rel := range []string{"a.md", "up/secret.md", "../secret.md", "alias.md"} {
		if b, err := (piece{notes, rel}).read(maxMarkdown); err == nil {
			t.Errorf("%s read %q", rel, b)
		}
	}
	put(t, filepath.Join(notes, "in.md"), "inside")
	if b, err := (piece{notes, "in.md"}).read(maxMarkdown); err != nil || string(b) != "inside" {
		t.Errorf("a file inside: %q, %v", b, err)
	}
}

// A book of one file has no list of its parts.
func TestOneFileBookHasNoList(t *testing.T) {
	notes := t.TempDir()
	put(t, filepath.Join(notes, "a.md"), "# A\n\nbody\n")
	f := withRoutes(t, route{Path: notes})
	if err := f.sendRoute("route:0:a.md"); err != nil {
		t.Fatal(err)
	}
	it, _ := f.findRoute("route:0:a.md")
	if strings.Contains(bookText(t, filepath.Join(f.Library, it.tab, it.name)), "<ul>") {
		t.Error("a one-file book lists its parts")
	}
}

// The browser's messages reach a route's book through answer, as the page sends them.
func TestAnswerRoutes(t *testing.T) {
	f := setup(t)
	notes := t.TempDir()
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	f.Routes = []route{{Path: notes}}
	config := filepath.Join(t.TempDir(), "folders.json")
	c, _ := json.Marshal(f)
	put(t, config, string(c))
	res := answer(message(`{"action":"list"}`), config)
	if !res.OK || len(res.Books) != 1 || res.Books[0].Name != "route:0:a.md" || !res.Books[0].Route {
		t.Fatalf("list: %+v", res)
	}
	if res := answer(message(`{"action":"send","file":"route:0:a.md"}`), config); !res.OK {
		t.Errorf("send: %+v", res)
	}
	if res := answer(message(`{"action":"list"}`), config); len(res.Books) != 1 || !res.Books[0].Sent {
		t.Errorf("after send: %+v", res)
	}
	if res := answer(message(`{"action":"unsend","file":"route:0:a.md"}`), config); !res.OK {
		t.Errorf("unsend: %+v", res)
	}
	it, _ := f.findRoute("route:0:a.md")
	if exists(filepath.Join(f.Library, it.tab, it.name)) || listed(t, f)["route:0:a.md"].Sent {
		t.Error("an unticked book is still made or sent")
	}
}

// Guard 4: two places a slug folds together get two names.
func TestNamesDoNotCollide(t *testing.T) {
	notes := t.TempDir()
	os.MkdirAll(filepath.Join(notes, "a-b"), 0o755)
	os.MkdirAll(filepath.Join(notes, "a"), 0o755)
	put(t, filepath.Join(notes, "a-b", "index.md"), "# One\n\none_word\n")
	put(t, filepath.Join(notes, "a", "b-index.md"), "# Two\n\ntwo_word\n")
	f := withRoutes(t, route{Path: notes})
	one, _ := f.findRoute("route:0:a-b/index.md")
	two, _ := f.findRoute("route:0:a/b-index.md")
	if one.name == two.name || one.name == "" {
		t.Fatalf("%q and %q", one.name, two.name)
	}
	f.sendRoute(one.id)
	f.sendRoute(two.id)
	if !strings.Contains(bookText(t, filepath.Join(f.Sync, one.tab, one.name)), "one_word") {
		t.Error("one book overwrote the other")
	}
	if again, _ := f.findRoute(one.id); again.name != one.name {
		t.Error("a name changed between two walks")
	}
}

// Guard 5: a route as wide as the home folder or the disk, or a name that climbs,
// is refused.
func TestRoutesAreChecked(t *testing.T) {
	for _, r := range []route{
		{Path: "~"}, {Path: "~/"}, {Path: "/home/reader"}, {Path: "/"}, {Path: "notes"},
		{Path: "/n", Tab: "a/b"}, {Path: "/n", Tab: ".."}, {Path: "/n", Tab: ".ink"},
		{Path: "/n", Book: "../x.md"}, {Path: "/n", Book: "a.md", Files: []string{"b.md"}},
		{Path: "/n", Rounds: "("}, {Path: "/n", Files: []string{"../x.md"}}, {Path: "/n", Files: []string{"/etc/x.md"}},
		{Path: "/n", Files: []string{"notes.txt"}},
		{Path: "/n", Files: []string{"~/secret.md"}},
		{Path: "/home"}, {Path: "/home/reader/.."}, {Path: "~/.."},
		{Path: "/n/.secret"}, {Path: "/n", Tab: "/"},
		{Path: "/home/reader/.ssh/notes"}, {Path: "/n", Files: []string{".git/x.md"}},
	} {
		if err := checkRoutes([]route{r}, "/home/reader"); err == nil {
			t.Errorf("%+v was accepted", r)
		}
	}
	ok := []route{{Path: "~/notes", Files: []string{"x.md", "sub/y.md"}}}
	if err := checkRoutes(ok, "/home/reader"); err != nil || ok[0].Path != "/home/reader/notes" || ok[0].Files[1] != "sub/y.md" || ok[0].Tab != defaultTab {
		t.Errorf("got %+v, %v", ok, err)
	}
}

// A route that is gone, a drive not mounted, offers nothing and breaks nothing.
func TestMissingRoute(t *testing.T) {
	notes := t.TempDir()
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	f := withRoutes(t, route{Path: filepath.Join(notes, "gone")}, route{Path: notes})
	if got := listed(t, f); len(got) != 1 || got["route:1:a.md"].Name == "" {
		t.Errorf("listed %v", got)
	}
}

func TestBookFiles(t *testing.T) {
	files := []string{"r/9-a/old.md", "r/10-b/new.md", "r/overview.md", "r/10-b/x/deep.md", "s/other.md"}
	got := bookFiles(files, "r/overview.md", regexpMust(`^(\d+)-`), nil)
	if strings.Join(got, " ") != "r/overview.md r/10-b/new.md" {
		t.Errorf("got %v", got)
	}
	if got := bookFiles([]string{"x.md", "overview.md"}, "overview.md", nil, nil); got[0] != "overview.md" {
		t.Errorf("the book's own file is not first: %v", got)
	}
}

func TestEPUBSourceLink(t *testing.T) {
	notes := t.TempDir()
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	f := withRoutes(t, route{Path: notes})
	f.sendRoute("route:0:a.md")
	it, _ := f.findRoute("route:0:a.md")
	if text := bookText(t, filepath.Join(f.Library, it.tab, it.name)); strings.Contains(text, "<a href") {
		t.Error("a book with no source has a link to one")
	}
}

func regexpMust(s string) *regexp.Regexp { return regexp.MustCompile(s) }

// A symlinked folder in the sync folder leads nowhere: send and unsend stay inside
// it whatever it holds.
func TestSyncLinkLeadsNowhere(t *testing.T) {
	f := setup(t)
	outside := t.TempDir()
	put(t, filepath.Join(outside, "x.epub"), "keep")
	os.Mkdir(filepath.Join(f.Library, "sub"), 0o755)
	put(t, filepath.Join(f.Library, "sub", "x.epub"), "book")
	os.Symlink(outside, filepath.Join(f.Sync, "sub"))
	if err := f.send("sub/x.epub"); err == nil {
		t.Error("send went through a link in the sync folder")
	}
	if err := f.unsend("sub/x.epub"); err == nil {
		t.Error("unsend went through a link in the sync folder")
	}
	if b, err := os.ReadFile(filepath.Join(outside, "x.epub")); err != nil || string(b) != "keep" {
		t.Errorf("the file outside became %q, %v", b, err)
	}
	// A name the folder outside does not hold yet is not linked there either.
	put(t, filepath.Join(f.Library, "sub", "y.epub"), "new")
	if err := f.send("sub/y.epub"); err == nil || exists(filepath.Join(outside, "y.epub")) {
		t.Errorf("a new book went through a link in the sync folder: %v", err)
	}
}

// A tab folder in the library that is a link, or a library that is not there,
// takes no book: nothing is written outside the library, and no library is made.
func TestLibraryLinkLeadsNowhere(t *testing.T) {
	notes := t.TempDir()
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	f := withRoutes(t, route{Path: notes})
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(f.Library, defaultTab))
	if err := f.sendRoute("route:0:a.md"); err == nil {
		t.Error("a book was made through a link in the library")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("outside the library: %v", entries)
	}
	g := withRoutes(t, route{Path: notes})
	g.Library = filepath.Join(t.TempDir(), "missing")
	if err := g.sendRoute("route:0:a.md"); err == nil || exists(g.Library) {
		t.Errorf("a missing library: %v, made %v", err, exists(g.Library))
	}
}

// What a route cannot read is left out, and every other book is still listed.
func TestUnreadableIsLeftOut(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not bind root")
	}
	notes := t.TempDir()
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	put(t, filepath.Join(notes, "closed.md"), "# Closed\n")
	os.Chmod(filepath.Join(notes, "closed.md"), 0)
	os.Mkdir(filepath.Join(notes, "locked"), 0o755)
	put(t, filepath.Join(notes, "locked", "in.md"), "# In\n")
	os.Chmod(filepath.Join(notes, "locked"), 0)
	t.Cleanup(func() { os.Chmod(filepath.Join(notes, "locked"), 0o755) })
	other := t.TempDir()
	put(t, filepath.Join(other, "real.md"), "# Real\n")
	os.Symlink(filepath.Join(other, "real.md"), filepath.Join(notes, "single.md"))
	f := withRoutes(t,
		route{Path: notes},
		route{Path: filepath.Join(t.TempDir(), "unmounted"), Files: []string{"x.md"}},
		route{Path: notes, Files: []string{"a.md", "closed.md"}},
		route{Path: filepath.Join(notes, "single.md")},
	)
	put(t, filepath.Join(f.Library, "saved.epub"), "s")
	got := listed(t, f)
	if len(got) != 2 || got["route:0:a.md"].Name == "" || got["saved.epub"].Name == "" {
		t.Errorf("listed %v", got)
	}
}

// A file name is text in the book, never markup.
func TestPartNamesAreText(t *testing.T) {
	notes := t.TempDir()
	put(t, filepath.Join(notes, "a<textarea>.md"), "alpha_word\n")
	put(t, filepath.Join(notes, "b.md"), "beta_word\n")
	f := withRoutes(t, route{Path: notes, Files: []string{"a<textarea>.md", "b.md"}})
	if err := f.sendRoute("route:0:"); err != nil {
		t.Fatal(err)
	}
	it, _ := f.findRoute("route:0:")
	text := bookText(t, filepath.Join(f.Library, it.tab, it.name))
	if !strings.Contains(text, "alpha_word") || !strings.Contains(text, "beta_word") || !strings.Contains(text, "a&lt;textarea&gt;") {
		t.Error("a file name swallowed the book")
	}
}

// A file listed in a route that is a link is never read, wherever it sits in the
// list, so the book is left out.
func TestFilesRouteLinkIsLeftOut(t *testing.T) {
	notes := t.TempDir()
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	outside := filepath.Join(t.TempDir(), "secret.md")
	put(t, outside, "secret_word\n")
	os.Symlink(outside, filepath.Join(notes, "link.md"))
	f := withRoutes(t, route{Path: notes, Files: []string{"a.md", "link.md"}})
	if got := listed(t, f); len(got) != 0 {
		t.Errorf("listed %v", got)
	}
	if err := f.sendRoute("route:0:"); err == nil {
		t.Error("a book holding a link was made")
	}
}

// A book route whose folder holds a file that cannot be read is left out, as a
// files route is, rather than listed and failing on the tick.
func TestBookRouteWithUnreadablePart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not bind root")
	}
	reviews := t.TempDir()
	os.Mkdir(filepath.Join(reviews, "g"), 0o755)
	put(t, filepath.Join(reviews, "g", "overview.md"), "# G\n")
	put(t, filepath.Join(reviews, "g", "closed.md"), "closed\n")
	os.Chmod(filepath.Join(reviews, "g", "closed.md"), 0)
	f := withRoutes(t, route{Path: reviews, Book: "overview.md"})
	if got := listed(t, f); len(got) != 0 {
		t.Errorf("listed %v", got)
	}
}

// Unticking a book whose library copy is already gone still takes it out of the
// sync folder.
func TestUntickWithoutLibraryCopy(t *testing.T) {
	notes := t.TempDir()
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	f := withRoutes(t, route{Path: notes})
	if err := f.sendRoute("route:0:a.md"); err != nil {
		t.Fatal(err)
	}
	it, _ := f.findRoute("route:0:a.md")
	os.Remove(filepath.Join(f.Library, it.tab, it.name))
	if err := f.unsendRoute("route:0:a.md"); err != nil || exists(filepath.Join(f.Sync, it.tab, it.name)) {
		t.Errorf("untick: %v, still in the sync folder %v", err, exists(filepath.Join(f.Sync, it.tab, it.name)))
	}
}

// A route that is a link to the home folder is refused like the home folder.
func TestRouteLinkToHome(t *testing.T) {
	home := t.TempDir()
	link := filepath.Join(t.TempDir(), "home")
	os.Symlink(home, link)
	if err := checkRoutes([]route{{Path: link}}, home); err == nil {
		t.Error("a link to the home folder was accepted")
	}
}

// A file route is a Markdown file; any other file is not offered.
func TestFileRouteIsMarkdown(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "key"), "secret")
	put(t, filepath.Join(dir, "a.md"), "# A\n")
	f := withRoutes(t, route{Path: filepath.Join(dir, "key")}, route{Path: filepath.Join(dir, "a.md")})
	if got := listed(t, f); len(got) != 1 || got["route:1:a.md"].Name == "" {
		t.Errorf("listed %v", got)
	}
}

// A Markdown file over the cap is left out.
func TestMarkdownOverCap(t *testing.T) {
	notes := t.TempDir()
	put(t, filepath.Join(notes, "big.md"), "# Big\n"+strings.Repeat("a", maxMarkdown))
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	f := withRoutes(t, route{Path: notes})
	if got := listed(t, f); len(got) != 1 || got["route:0:a.md"].Name == "" {
		t.Errorf("listed %v", got)
	}
}

// A route that is a link to a hidden folder is refused like a hidden route.
func TestRouteLinkToHidden(t *testing.T) {
	home := t.TempDir()
	os.Mkdir(filepath.Join(home, ".ssh"), 0o755)
	link := filepath.Join(home, "notes")
	os.Symlink(filepath.Join(home, ".ssh"), link)
	if err := checkRoutes([]route{{Path: link}}, "/elsewhere"); err == nil {
		t.Error("a link to a hidden folder was accepted")
	}
}

// No link is followed on the way to a file either: a files entry through a link
// to a hidden folder is never read.
func TestFilesEntryThroughALink(t *testing.T) {
	notes := t.TempDir()
	os.Mkdir(filepath.Join(notes, ".hidden"), 0o755)
	put(t, filepath.Join(notes, ".hidden", "h.md"), "hidden_word\n")
	os.Symlink(".hidden", filepath.Join(notes, "link"))
	f := withRoutes(t, route{Path: notes, Files: []string{"link/h.md"}})
	if got := listed(t, f); len(got) != 0 {
		t.Errorf("listed %v", got)
	}
	if err := f.sendRoute("route:0:"); err == nil {
		t.Error("a book was made through a link")
	}
}

// A folder route never offers a hidden Markdown file.
func TestHiddenMarkdownIsLeftOut(t *testing.T) {
	notes := t.TempDir()
	put(t, filepath.Join(notes, ".secret.md"), "# Secret\n")
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	f := withRoutes(t, route{Path: notes})
	if got := listed(t, f); len(got) != 1 || got["route:0:a.md"].Name == "" {
		t.Errorf("listed %v", got)
	}
}

// A book is left out when its files are over the cap together, not only one by one.
func TestBookOverCapInAll(t *testing.T) {
	notes := t.TempDir()
	half := strings.Repeat("a", maxMarkdown/2+1)
	put(t, filepath.Join(notes, "a.md"), half)
	put(t, filepath.Join(notes, "b.md"), half)
	f := withRoutes(t, route{Path: notes, Files: []string{"a.md", "b.md"}})
	if got := listed(t, f); len(got) != 0 {
		t.Errorf("listed %v", got)
	}
}

// A route whose own name is hidden is refused even when it links to a visible folder.
func TestHiddenRouteName(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "visible"), 0o755)
	os.Symlink(filepath.Join(dir, "visible"), filepath.Join(dir, ".alias"))
	if err := checkRoutes([]route{{Path: filepath.Join(dir, ".alias")}}, "/elsewhere"); err == nil {
		t.Error("a hidden route name was accepted")
	}
}

// A note that would take too much memory to make into a book is refused, and the
// refusal comes in time: a table of many columns grows with the square of its size.
func TestHeavyNoteIsRefused(t *testing.T) {
	savedHeap, savedTime := maxHeap, maxBuild
	t.Cleanup(func() { maxHeap, maxBuild = savedHeap, savedTime })
	maxHeap, maxBuild = 64<<20, 10*time.Second
	notes := t.TempDir()
	put(t, filepath.Join(notes, "t.md"), strings.Repeat("|a", 1000)+"\n"+strings.Repeat("|-", 1000)+"\n"+strings.Repeat("x\n", 1000))
	f := withRoutes(t, route{Path: notes})
	start := time.Now()
	if err := f.sendRoute("route:0:t.md"); !errors.Is(err, errTooHeavy) {
		t.Errorf("a heavy note: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the refusal took %v", took)
	}
	maxHeap, maxBuild = savedHeap, 50*time.Millisecond
	put(t, filepath.Join(notes, "slow.md"), strings.Repeat("[a](", 16<<10))
	if err := f.sendRoute("route:0:slow.md"); !errors.Is(err, errTooHeavy) {
		t.Errorf("a slow note: %v", err)
	}
}

// A library book named like a route id is taken back as a book too.
func TestUnsendBookNamedLikeARoute(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.Library, "route:x.epub"), "r")
	f.send("route:x.epub")
	if _, err := f.handle(request{Action: "unsend", File: "route:x.epub"}); err != nil || exists(filepath.Join(f.Sync, "route:x.epub")) {
		t.Errorf("unsend: %v", err)
	}
}

// A walk follows no link: a link back to the route is not walked into forever.
func TestWalkFollowsNoLink(t *testing.T) {
	notes := t.TempDir()
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	os.Symlink(".", filepath.Join(notes, "loop"))
	os.Mkdir(filepath.Join(notes, "real"), 0o755)
	put(t, filepath.Join(notes, "real", "b.md"), "# B\n")
	os.Symlink("real", filepath.Join(notes, "linkdir"))
	f := withRoutes(t, route{Path: notes})
	got := listed(t, f)
	if len(got) != 2 || got["route:0:a.md"].Name == "" || got["route:0:real/b.md"].Name == "" {
		t.Errorf("listed %v", got)
	}
}

// A tab folder that links to another folder inside the library takes no write:
// nothing is made through it, and the book is not listed twice.
func TestTabLinkInsideTheLibrary(t *testing.T) {
	notes := t.TempDir()
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	f := withRoutes(t, route{Path: notes})
	os.Mkdir(filepath.Join(f.Library, "other"), 0o755)
	os.Symlink("other", filepath.Join(f.Library, defaultTab))
	if err := f.sendRoute("route:0:a.md"); err == nil {
		t.Error("a book was made through a link in the library")
	}
	if entries, _ := os.ReadDir(filepath.Join(f.Library, "other")); len(entries) != 0 {
		t.Errorf("the linked folder holds %v", entries)
	}
}

// A walk stops maxDepth folders down.
func TestWalkDepth(t *testing.T) {
	notes := t.TempDir()
	deep := notes
	for i := 0; i <= maxDepth; i++ {
		deep = filepath.Join(deep, "d")
		os.Mkdir(deep, 0o755)
		put(t, filepath.Join(deep, "n.md"), "# N\n")
	}
	f := withRoutes(t, route{Path: notes})
	if got := listed(t, f); len(got) != maxDepth {
		t.Errorf("listed %d books, want %d", len(got), maxDepth)
	}
}
