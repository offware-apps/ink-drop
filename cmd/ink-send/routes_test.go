package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
		if !ok || b.Title != title || !b.Route || b.Sent != nil {
			t.Errorf("%s: %+v", id, b)
		}
	}
	if got["route:1:soup.md"].Folder != "food" || got["route:0:idea.md"].Folder != defaultTab {
		t.Errorf("tabs: %+v", got)
	}

	// A listed file order is the book's order.
	if err := f.send("route:2:", "Tablet"); err == nil {
		t.Error("a route's id went through the plain send")
	}
	if err := f.sendRoute("route:2:", "Tablet"); err != nil {
		t.Fatal(err)
	}
	it, _ := f.findRoute("route:2:")
	text := bookText(t, filepath.Join(f.Transmitters[0].Folder, it.tab, it.name))
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
	if err := f.sendRoute(b.Name, "Tablet"); err != nil {
		t.Fatal(err)
	}
	it, _ := f.findRoute(b.Name)
	sent := filepath.Join(f.Transmitters[0].Folder, "reviews", it.name)
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
	if got := listed(t, f)[b.Name]; len(got.Sent) != 1 || got.Size == 0 {
		t.Errorf("after the tick: %+v", got)
	}
	if listed(t, f)["reviews/"+it.name].Name != "" {
		t.Error("the made book is listed twice")
	}

	// Unticking takes it from the transmitter and the library; the route never
	// changes.
	if err := f.unsendRoute(b.Name, "Tablet"); err != nil {
		t.Fatal(err)
	}
	if exists(sent) || exists(filepath.Join(f.Library, "reviews", it.name)) || exists(filepath.Join(f.Library, "reviews", ".ink", it.name+".json")) {
		t.Error("an unticked book is still made")
	}
	if after := snapshot(t, reviews); after != before {
		t.Errorf("the route changed:\n%s\n%s", before, after)
	}
}

// A second transmitter holding the book keeps it made.
func TestUntickKeepsABookAnotherHolds(t *testing.T) {
	notes := t.TempDir()
	put(t, filepath.Join(notes, "a.md"), "# A\n")
	f := withRoutes(t, route{Path: notes})
	usb := t.TempDir()
	f.Transmitters = append(f.Transmitters, transmitter{Name: "USB", Folder: usb})
	f.sendRoute("route:0:a.md", "Tablet")
	f.sendRoute("route:0:a.md", "USB")
	if err := f.unsendRoute("route:0:a.md", "Tablet"); err != nil {
		t.Fatal(err)
	}
	it, _ := f.findRoute("route:0:a.md")
	if !exists(filepath.Join(f.Library, it.tab, it.name)) || !exists(filepath.Join(usb, it.tab, it.name)) {
		t.Error("unticking one transmitter took the book from the other")
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
		if err := f.sendRoute(id, "Tablet"); err == nil {
			t.Errorf("%q was accepted", id)
		}
		if err := f.unsendRoute(id, "Tablet"); err == nil {
			t.Errorf("unsend %q was accepted", id)
		}
	}
	if entries, _ := os.ReadDir(f.Transmitters[0].Folder); len(entries) != 0 {
		t.Errorf("the transmitter holds %v", entries)
	}
	if err := f.sendRoute("route:0:a.md", "Nobody"); err == nil {
		t.Error("a transmitter nobody wrote was accepted")
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
	f.sendRoute("route:0:a.md", "Tablet")
	filepath.WalkDir(f.Library, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".epub") && strings.Contains(bookText(t, p), "secret_word") {
			t.Errorf("%s holds a file from outside the route", p)
		}
		return nil
	})

	// The same holds for a file a route lists by name.
	g := withRoutes(t, route{Path: notes, Files: []string{"linked/secret.md"}})
	if err := g.sendRoute("route:0:", "Tablet"); err == nil {
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
	for _, rel := range []string{"a.md", "up/secret.md", "../secret.md"} {
		if b, err := (piece{notes, rel}).read(); err == nil {
			t.Errorf("%s read %q", rel, b)
		}
	}
	put(t, filepath.Join(notes, "in.md"), "inside")
	if b, err := (piece{notes, "in.md"}).read(); err != nil || string(b) != "inside" {
		t.Errorf("a file inside: %q, %v", b, err)
	}
}

// A book of one file has no list of its parts.
func TestOneFileBookHasNoList(t *testing.T) {
	notes := t.TempDir()
	put(t, filepath.Join(notes, "a.md"), "# A\n\nbody\n")
	f := withRoutes(t, route{Path: notes})
	if err := f.sendRoute("route:0:a.md", "Tablet"); err != nil {
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
	if res := answer(message(`{"action":"send","file":"route:0:a.md","to":"Tablet"}`), config); !res.OK {
		t.Errorf("send: %+v", res)
	}
	if res := answer(message(`{"action":"list"}`), config); len(res.Books) != 1 || len(res.Books[0].Sent) != 1 {
		t.Errorf("after send: %+v", res)
	}
	if res := answer(message(`{"action":"unsend","file":"route:0:a.md","to":"Tablet"}`), config); !res.OK {
		t.Errorf("unsend: %+v", res)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.Library, defaultTab)); len(entries) != 1 {
		t.Errorf("after unsend the library holds %v", entries)
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
	f.sendRoute(one.id, "Tablet")
	f.sendRoute(two.id, "Tablet")
	if !strings.Contains(bookText(t, filepath.Join(f.Transmitters[0].Folder, one.tab, one.name)), "one_word") {
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
	} {
		if err := checkRoutes([]route{r}, "/home/reader"); err == nil {
			t.Errorf("%+v was accepted", r)
		}
	}
	ok := []route{{Path: "~/notes", Files: []string{"~/other/x.md", "sub/y.md"}}}
	if err := checkRoutes(ok, "/home/reader"); err != nil || ok[0].Path != "/home/reader/notes" || ok[0].Files[0] != "/home/reader/other/x.md" || ok[0].Tab != defaultTab {
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
	f.sendRoute("route:0:a.md", "Tablet")
	it, _ := f.findRoute("route:0:a.md")
	if text := bookText(t, filepath.Join(f.Library, it.tab, it.name)); strings.Contains(text, "<a href") {
		t.Error("a book with no source has a link to one")
	}
}

func regexpMust(s string) *regexp.Regexp { return regexp.MustCompile(s) }
