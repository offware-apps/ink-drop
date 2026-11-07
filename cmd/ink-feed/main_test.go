package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/offware-apps/ink-drop/folder"
)

var png = []byte("\x89PNG\r\n\x1a\n0000")

const rssItem = `<item><title>%s</title><link>/posts/%s</link><guid>%s</guid>
<description>Short %s.</description>
<content:encoded><![CDATA[<p>Full %s.</p><img src="img.png"><a href="next">next</a>]]></content:encoded></item>`

// server serves the RSS feed made of items at /rss.xml, an Atom feed at
// /atom.xml, and one image under /posts/.
func server(items *[]string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/rss.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><title>A blog</title>`))
		for _, id := range *items {
			w.Write([]byte(strings.ReplaceAll(rssItem, "%s", id)))
		}
		w.Write([]byte(`</channel></rss>`))
	})
	mux.HandleFunc("/atom.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0" encoding="ISO-8859-1"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Un journal</title>
<entry><title>Caf` + "\xe9" + `</title><link rel="alternate" href="https://example.org/cafe"/><id>urn:1</id><updated>2026-10-01T00:00:00Z</updated>
<content type="xhtml"><div xmlns="http://www.w3.org/1999/xhtml"><p>Une <em>tasse</em>.</p></div></content></entry>
<entry><title>Plain</title><id>urn:2</id><summary type="text">1 &lt; 2 &amp; &lt;b&gt;not bold&lt;/b&gt;</summary></entry>
<entry><title>Marked</title><id>urn:3</id><content type="html">&lt;i&gt;slanted&lt;/i&gt;</content></entry>
</feed>`))
	})
	mux.HandleFunc("/page.html", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><head><link rel="stylesheet" type="application/rss+xml" href="rss.xml"></head><body><p>Not a feed.</p></body></html>`))
	})
	mux.HandleFunc("/note.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0"?><note><title>Not a feed</title><item>x</item></note>`))
	})
	mux.HandleFunc("/site/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><head><link rel="alternate" hreflang="fr" type="text/html" href="/fr/"><link rel="Alternate" type="application/rss+xml" href="../rss.xml"></head><body>A site.</body></html>`))
	})
	mux.HandleFunc("/posts/img.png", func(w http.ResponseWriter, r *http.Request) { w.Write(png) })
	return httptest.NewServer(mux)
}

func read(t *testing.T, paths []string) string {
	t.Helper()
	if len(paths) != 1 {
		t.Fatalf("want one digest, got %v", paths)
	}
	b, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFeedRSS(t *testing.T) {
	items := []string{"one"}
	srv := server(&items)
	defer srv.Close()
	paths, err := digest(srv.Client(), t.TempDir(), srv.URL+"/rss.xml", false)
	if err != nil {
		t.Fatal(err)
	}
	page := read(t, paths)
	if !strings.Contains(page, "Full one.") || strings.Contains(page, "Short one.") {
		t.Errorf("want the full content and not the summary, got %s", page)
	}
	if !strings.Contains(page, "data:image/png;base64,") || !strings.Contains(page, `href="`+srv.URL+`/posts/next"`) {
		t.Errorf("want the image inlined and the link made absolute against the entry, got %s", page)
	}
	m, err := folder.Get(paths[0])
	// A digest's source is the feed's address, a fragment naming the run.
	if err != nil || !strings.HasPrefix(m.Source, srv.URL+"/rss.xml#digest-") || m.Stage != "ink-feed" || !strings.HasPrefix(m.Title, "A blog, ") {
		t.Errorf("sidecar %+v, %v", m, err)
	}
}

func TestFeedAtom(t *testing.T) {
	srv := server(nil)
	defer srv.Close()
	paths, err := digest(srv.Client(), t.TempDir(), srv.URL+"/atom.xml", false)
	if err != nil {
		t.Fatal(err)
	}
	page := read(t, paths)
	for _, want := range []string{"Café", `href="https://example.org/cafe"`, "<em>tasse</em>", "2026-10-01", "1 &lt; 2 &amp; &lt;b&gt;not bold&lt;/b&gt;", "<i>slanted</i>"} {
		if !strings.Contains(page, want) {
			t.Errorf("digest lacks %q: %s", want, page)
		}
	}
}

func TestFeedNewEntriesOnly(t *testing.T) {
	items := []string{"one", "two"}
	srv := server(&items)
	defer srv.Close()
	dir := t.TempDir()
	run := func() []string {
		paths, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", false)
		if err != nil {
			t.Fatal(err)
		}
		return paths
	}
	read(t, run())
	if paths := run(); paths != nil {
		t.Fatalf("an unchanged feed wrote %v", paths)
	}
	items = []string{"three", "one", "two"}
	if page := read(t, run()); !strings.Contains(page, "Full three.") || strings.Contains(page, "Full one.") {
		t.Errorf("want the added entry alone, got %s", page)
	}
	if paths := run(); paths != nil {
		t.Fatalf("an unchanged feed wrote %v", paths)
	}
}

func TestFeedStatePerURL(t *testing.T) {
	items := []string{"one"}
	srv := server(&items)
	defer srv.Close()
	dir := t.TempDir()
	for _, q := range []string{"?c=A", "?c=a"} {
		paths, err := digest(srv.Client(), dir, srv.URL+"/rss.xml"+q, false)
		if err != nil {
			t.Fatal(err)
		}
		read(t, paths)
	}
}

func TestFeedFromSite(t *testing.T) {
	items := []string{"one"}
	srv := server(&items)
	defer srv.Close()
	paths, err := digest(srv.Client(), t.TempDir(), srv.URL+"/site/", false)
	if err != nil {
		t.Fatal(err)
	}
	if page := read(t, paths); !strings.Contains(page, "Full one.") {
		t.Errorf("want the feed the site links to, got %s", page)
	}
	if m, _ := folder.Get(paths[0]); !strings.HasPrefix(m.Source, srv.URL+"/rss.xml#") {
		t.Errorf("sidecar source %q, want the feed's own address", m.Source)
	}
}

func TestFeedRejectsPage(t *testing.T) {
	items := []string{"one"}
	srv := server(&items)
	defer srv.Close()
	for _, p := range []string{"/page.html", "/note.xml"} {
		if _, err := digest(srv.Client(), t.TempDir(), srv.URL+p, false); err == nil {
			t.Errorf("%s is no feed and links to none, yet passed as one", p)
		}
	}
}

func TestFeedStateWaitsForDigest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only folder")
	}
	items := []string{"one"}
	srv := server(&items)
	defer srv.Close()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, folder.SidecarDir, "feeds"), 0o755)
	os.Chmod(dir, 0o555)
	if _, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", false); err == nil {
		t.Fatal("a digest landed in a read-only folder")
	}
	os.Chmod(dir, 0o755)
	paths, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", false)
	if err != nil {
		t.Fatal(err)
	}
	read(t, paths)
}

func TestFeedEach(t *testing.T) {
	items := []string{"one", "two"}
	srv := server(&items)
	defer srv.Close()
	paths, err := digest(srv.Client(), t.TempDir(), srv.URL+"/rss.xml", true)
	if err != nil || len(paths) != 2 {
		t.Fatalf("want one item per entry, got %v, %v", paths, err)
	}
	for i, id := range items {
		page := read(t, paths[i:i+1])
		other := items[1-i]
		if !strings.Contains(page, "Full "+id+".") || strings.Contains(page, "Full "+other+".") {
			t.Errorf("item %d holds the wrong entry: %s", i, page)
		}
		m, err := folder.Get(paths[i])
		// The guid is not the link, so the source carries it.
		if err != nil || m.Title != id || m.Source != srv.URL+"/posts/"+id+"#"+id {
			t.Errorf("sidecar %+v, %v", m, err)
		}
	}
}

// summaries serves a feed whose two entries carry a summary alone, and a page
// for each: a long article, and one thinner than its summary.
func summaries() *httptest.Server {
	long := strings.Repeat("The whole article goes on and on. ", 80)
	mux := http.NewServeMux()
	mux.HandleFunc("/rss.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>News</title>
<item><title>Long</title><link>/long</link><guid>1</guid><description>A summary of the long one.</description></item>
<item><title>Walled</title><link>/walled</link><guid>2</guid><description>A summary of the walled one, longer than its page.</description></item>
</channel></rss>`))
	})
	mux.HandleFunc("/long", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><head><title>Long</title></head><body><nav>Menu</nav><article><h1>Long</h1><p>` + long + `</p><p>` + long + `</p></article></body></html>`))
	})
	mux.HandleFunc("/walled", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><article><p>Please log in.</p></article></body></html>`))
	})
	return httptest.NewServer(mux)
}

func TestFeedFetchesWholeArticle(t *testing.T) {
	srv := summaries()
	defer srv.Close()
	paths, err := digest(srv.Client(), t.TempDir(), srv.URL+"/rss.xml", false)
	if err != nil {
		t.Fatal(err)
	}
	page := read(t, paths)
	if !strings.Contains(page, "The whole article goes on") || strings.Contains(page, "A summary of the long one.") {
		t.Errorf("want the long entry's article in place of its summary, got %s", page)
	}
	if !strings.Contains(page, "A summary of the walled one") || strings.Contains(page, "Please log in.") {
		t.Errorf("want the walled entry's summary kept, got %s", page)
	}
	if !strings.Contains(page, `<a href="#entry-2">Walled</a>`) || !strings.Contains(page, `<h2 id="entry-2">Walled</h2>`) {
		t.Errorf("want a contents list linking to each entry, got %s", page)
	}
}

// many serves a feed of n entries carrying a summary alone, every one linking
// to a long article, and counts the requests for the feed itself.
func many(n int, feedHits *int) *httptest.Server {
	long := strings.Repeat("The whole article goes on and on. ", 80)
	mux := http.NewServeMux()
	mux.HandleFunc("/rss.xml", func(w http.ResponseWriter, r *http.Request) {
		*feedHits++
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Many</title>`))
		for i := range n {
			fmt.Fprintf(w, `<item><title>Entry %d</title><link>/a/%d</link><guid>%d</guid><description>Short.</description></item>`, i, i, i)
		}
		w.Write([]byte(`<item><title>No link</title><guid>nolink</guid><description>Short.</description></item></channel></rss>`))
	})
	mux.HandleFunc("/a/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><article><p>` + long + `</p><p>` + long + `</p></article></body></html>`))
	})
	return httptest.NewServer(mux)
}

func TestFeedCapsEntriesPerRun(t *testing.T) {
	hits := 0
	srv := many(maxArticles+10, &hits)
	defer srv.Close()
	dir := t.TempDir()
	first, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true)
	if err != nil || len(first) != maxArticles {
		t.Fatalf("first run: want %d items, got %d, %v", maxArticles, len(first), err)
	}
	second, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true)
	if err != nil || len(second) != 11 {
		t.Fatalf("second run: want the 11 entries left, got %d, %v", len(second), err)
	}
	whole := 0
	for _, p := range append(first, second...) {
		if b, _ := os.ReadFile(p); strings.Contains(string(b), "The whole article goes on") {
			whole++
		}
	}
	if whole != maxArticles+10 {
		t.Errorf("want every linked entry whole, got %d of %d", whole, maxArticles+10)
	}
	if hits != 2 {
		t.Errorf("want the feed fetched once per run, got %d requests: an entry with no link fetched it", hits)
	}
	if third, _ := digest(srv.Client(), dir, srv.URL+"/rss.xml", true); len(third) != 0 {
		t.Errorf("third run: want nothing new, got %d", len(third))
	}
}

func TestLoneEntryHasNoAnchor(t *testing.T) {
	items := []string{"one"}
	srv := server(&items)
	defer srv.Close()
	paths, err := digest(srv.Client(), t.TempDir(), srv.URL+"/rss.xml", true)
	if err != nil {
		t.Fatal(err)
	}
	if page := read(t, paths); strings.Contains(page, `id="entry-`) {
		t.Errorf("a one-entry book anchors its heading, so its table of contents names it twice: %s", page)
	}
}

// A page an earlier run left waiting keeps its file when a new entry takes
// the same name: titles in Cyrillic all slug to "item".
func TestEachKeepsWaitingPage(t *testing.T) {
	titles := []string{"Новости дня"}
	mux := http.NewServeMux()
	mux.HandleFunc("/rss.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Вести</title>`))
		for i, title := range titles {
			fmt.Fprintf(w, `<item><title>%s</title><link>/n/%d</link><guid>%d</guid><content:encoded xmlns:content="http://purl.org/rss/1.0/modules/content/"><![CDATA[<p>%s day %d</p>]]></content:encoded></item>`, title, i, i, strings.Repeat("Long text. ", 200), i)
		}
		w.Write([]byte(`</channel></rss>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dir := t.TempDir()
	first, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true)
	if err != nil || len(first) != 1 {
		t.Fatalf("first run: %v, %v", first, err)
	}
	titles = append(titles, "Новости дня")
	second, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true)
	if err != nil || len(second) != 1 || second[0] == first[0] {
		t.Fatalf("second run wrote over the waiting page: first %v, second %v, %v", first, second, err)
	}
	if b, _ := os.ReadFile(first[0]); !strings.Contains(string(b), "day 0") {
		t.Errorf("the waiting page lost its entry: %s", b)
	}
}

// A page past maxArticle stays out, and its entry keeps the summary.
func TestArticleSizeCap(t *testing.T) {
	// A fixed figure, so the cap's own value is what the test holds: 9 MB.
	big := strings.Repeat("x", 9<<20)
	mux := http.NewServeMux()
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body><article><p>" + big + "</p></article></body></html>"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	u, _ := url.Parse(srv.URL + "/big")
	if _, err := article(srv.Client(), u); err == nil {
		t.Fatal("a page over maxArticle was kept")
	}
}

// linkless serves entries with a guid and no link, each titled title, for the
// ids in *ids.
func linkless(title string, ids *[]string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/rss.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><title>Podcast</title>`))
		for _, id := range *ids {
			fmt.Fprintf(w, `<item><title>%s</title><guid>%s</guid><content:encoded><![CDATA[<p>%s entry %s</p>]]></content:encoded></item>`, title, id, strings.Repeat("Long. ", 300), id)
		}
		w.Write([]byte(`</channel></rss>`))
	})
	return httptest.NewServer(mux)
}

// Two entries with no link and the same title are two pages, never one.
func TestLinklessEntriesStayApart(t *testing.T) {
	ids := []string{"a"}
	srv := linkless("Эпизод", &ids)
	defer srv.Close()
	dir := t.TempDir()
	first, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true)
	if err != nil || len(first) != 1 {
		t.Fatalf("first run: %v, %v", first, err)
	}
	ids = []string{"b", "a"}
	second, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true)
	if err != nil || len(second) != 1 || second[0] == first[0] {
		t.Fatalf("second run took the waiting page's name: %v then %v, %v", first, second, err)
	}
	if b, _ := os.ReadFile(first[0]); !strings.Contains(string(b), "entry a") {
		t.Errorf("the waiting page lost its entry")
	}
	a, _ := folder.Get(first[0])
	b, _ := folder.Get(second[0])
	if a.Source == b.Source {
		t.Errorf("both entries name the source %s", a.Source)
	}
}

// A run that fails part way keeps the entries it wrote: the next one writes
// only the rest.
func TestRunStoppedPartWayKeepsWhatItWrote(t *testing.T) {
	items := []string{"one", "two"}
	srv := server(&items)
	defer srv.Close()
	dir := t.TempDir()
	// A folder where the second item's sidecar goes makes its write fail.
	block := filepath.Join(dir, folder.SidecarDir, "two.html.json")
	if err := os.MkdirAll(block, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true); err == nil {
		t.Fatal("the blocked write did not fail")
	}
	os.Remove(block)
	again, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true)
	if err != nil || len(again) != 1 || filepath.Base(again[0]) != "two.html" {
		t.Fatalf("want only two.html again, got %v, %v", again, err)
	}
}

// shared serves entries that all link to one page and share one title, for
// the guids in *ids.
func shared(ids *[]string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/rss.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><title>Show</title>`))
		for _, id := range *ids {
			fmt.Fprintf(w, `<item><title>Выпуск</title><link>/show</link><guid isPermaLink="false">%s</guid><content:encoded><![CDATA[<p>%s episode %s</p>]]></content:encoded></item>`, id, strings.Repeat("Long. ", 300), id)
		}
		w.Write([]byte(`</channel></rss>`))
	})
	return httptest.NewServer(mux)
}

// Entries sharing one link and one title stay apart, in one run and across
// runs, each source naming its own entry.
func TestEntriesSharingALinkStayApart(t *testing.T) {
	ids := []string{"ep1", "ep2"}
	srv := shared(&ids)
	defer srv.Close()
	dir := t.TempDir()
	one, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true)
	if err != nil || len(one) != 2 || one[0] == one[1] {
		t.Fatalf("one run: %v, %v", one, err)
	}
	ids = []string{"ep3", "ep2", "ep1"}
	two, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true)
	if err != nil || len(two) != 1 || two[0] == one[0] || two[0] == one[1] {
		t.Fatalf("next run took a waiting page's name: %v then %v, %v", one, two, err)
	}
	sources := map[string]bool{}
	for _, p := range append(one, two...) {
		m, _ := folder.Get(p)
		sources[m.Source] = true
	}
	if len(sources) != 3 {
		t.Errorf("want three sources, got %v", sources)
	}
	for i, want := range []string{"episode ep1", "episode ep2"} {
		if b, _ := os.ReadFile(one[i]); !strings.Contains(string(b), want) {
			t.Errorf("%s lost its entry", one[i])
		}
	}
}

// An article's own ids opening on "entry-" never take a digest's anchors.
func TestArticleIDsStayItsOwn(t *testing.T) {
	u, _ := url.Parse("https://example.org/feed")
	page := string(render("News", []entry{{Title: "A"}, {Title: "B"}}, []string{`<p id="entry-2">a <a href="#entry-2">self</a></p>`, ""}, u))
	if strings.Count(page, `id="entry-2"`) != 1 || !strings.Contains(page, `id="in-entry-2"`) || !strings.Contains(page, `href="#in-entry-2"`) {
		t.Errorf("page %s", page)
	}
}

// feedOf serves an RSS feed of the given raw items at /rss.xml, with the
// channel title title, and the handlers in extra.
func feedOf(title string, items *string, extra map[string]http.HandlerFunc) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/rss.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><title>%s</title>%s</channel></rss>`, title, *items)
	})
	for p, h := range extra {
		mux.HandleFunc(p, h)
	}
	return httptest.NewServer(mux)
}

func long(s string) string {
	return `<content:encoded><![CDATA[<p>` + strings.Repeat("Long. ", 300) + s + `</p>]]></content:encoded>`
}

// Entries whose links do not parse, with no guid, stay apart.
func TestUnparsedLinksStayApart(t *testing.T) {
	items := `<item><title>Новость</title><link>/a%zz</link>` + long("entry a") + `</item>`
	srv := feedOf("Новости", &items, nil)
	defer srv.Close()
	dir := t.TempDir()
	first, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true)
	if err != nil || len(first) != 1 {
		t.Fatalf("%v, %v", first, err)
	}
	items = `<item><title>Новость</title><link>/b%zz</link>` + long("entry b") + `</item>` + items
	second, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true)
	if err != nil || len(second) != 1 || second[0] == first[0] {
		t.Fatalf("one page for two entries: %v then %v, %v", first, second, err)
	}
	if b, _ := os.ReadFile(first[0]); !strings.Contains(string(b), "entry a") {
		t.Errorf("entry a was overwritten")
	}
}

// Entries with no guid that all link to one page are each new once.
func TestGuidlessEntriesSharingALink(t *testing.T) {
	items := `<item><title>Day one</title><link>/today</link>` + long("one") + `</item>`
	srv := feedOf("Daily", &items, nil)
	defer srv.Close()
	dir := t.TempDir()
	if got, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true); err != nil || len(got) != 1 {
		t.Fatalf("%v, %v", got, err)
	}
	items = `<item><title>Day two</title><link>/today</link>` + long("two") + `</item>`
	got, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true)
	if err != nil || len(got) != 1 {
		t.Fatalf("day two never arrived: %v, %v", got, err)
	}
}

// Two feeds sharing a channel title keep both digests.
func TestDigestsOfOneTitle(t *testing.T) {
	a := `<item><title>A</title><guid>a</guid>` + long("from a") + `</item>`
	b := `<item><title>B</title><guid>b</guid>` + long("from b") + `</item>`
	sa, sb := feedOf("Site", &a, nil), feedOf("Site", &b, nil)
	defer sa.Close()
	defer sb.Close()
	dir := t.TempDir()
	pa, err := digest(sa.Client(), dir, sa.URL+"/rss.xml", false)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := digest(sb.Client(), dir, sb.URL+"/rss.xml", false)
	if err != nil || pa[0] == pb[0] {
		t.Fatalf("one digest for two feeds: %v, %v, %v", pa, pb, err)
	}
	if got, _ := os.ReadFile(pa[0]); !strings.Contains(string(got), "from a") {
		t.Errorf("feed a's digest was replaced")
	}
}

// An article a busy server refuses is tried again in the next run, and after
// maxTries runs its entry keeps the summary.
func TestBusyArticleIsTriedAgain(t *testing.T) {
	busy := true
	article := strings.Repeat("The whole article. ", 200)
	items := `<item><title>News</title><link>/a</link><guid>1</guid><description>A summary.</description></item>`
	srv := feedOf("Site", &items, map[string]http.HandlerFunc{"/a": func(w http.ResponseWriter, r *http.Request) {
		if busy {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("<html><body><article><p>" + article + "</p><p>" + article + "</p></article></body></html>"))
	}})
	defer srv.Close()
	dir := t.TempDir()
	if got, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true); err != nil || len(got) != 0 {
		t.Fatalf("a busy server's entry was written: %v, %v", got, err)
	}
	busy = false
	got, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true)
	if err != nil || len(got) != 1 {
		t.Fatalf("second run: %v, %v", got, err)
	}
	if b, _ := os.ReadFile(got[0]); !strings.Contains(string(b), "The whole article") {
		t.Errorf("the article did not come once the server was back")
	}
	// A server busy every time: the summary after maxTries runs.
	busy = true
	items = `<item><title>Other</title><link>/a</link><guid>2</guid><description>Another summary.</description></item>`
	for run := 1; run < maxTries; run++ {
		if got, _ := digest(srv.Client(), dir, srv.URL+"/rss.xml", true); len(got) != 0 {
			t.Fatalf("run %d wrote the entry before its tries ran out", run)
		}
	}
	got, err = digest(srv.Client(), dir, srv.URL+"/rss.xml", true)
	if err != nil || len(got) != 1 {
		t.Fatalf("after %d busy runs the entry never came: %v, %v", maxTries, got, err)
	}
	if b, _ := os.ReadFile(got[0]); !strings.Contains(string(b), "Another summary.") {
		t.Errorf("the entry did not keep its summary")
	}
}

// pending lists the items waiting in the folder, each with its sidecar.
func TestPendingListsWaitingItems(t *testing.T) {
	dir := t.TempDir()
	p, err := folder.Put(dir, "a.html", []byte("<p>a</p>"), folder.Meta{Source: "https://x/a", Stage: "ink-feed"})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "stray.html"), []byte("x"), 0o644)
	if got := pending(dir); len(got) != 1 || got[0] != p {
		t.Errorf("pending %v, want [%s]", got, p)
	}
}

// Entries with no guid on one link, titles slugging alike, stay apart.
func TestGuidlessOnOneLinkStayApart(t *testing.T) {
	items := `<item><title>Новость</title><link>/today</link>` + long("entry one") + `</item><item><title>Событие</title><link>/today</link>` + long("entry two") + `</item>`
	srv := feedOf("Сегодня", &items, nil)
	defer srv.Close()
	got, err := digest(srv.Client(), t.TempDir(), srv.URL+"/rss.xml", true)
	if err != nil || len(got) != 2 || got[0] == got[1] {
		t.Fatalf("one page for two entries: %v, %v", got, err)
	}
	for i, want := range []string{"entry one", "entry two"} {
		if b, _ := os.ReadFile(got[i]); !strings.Contains(string(b), want) {
			t.Errorf("%s lost %s", got[i], want)
		}
	}
}

// Mute and only words pick entries, without case or accents, and an entry
// left out never comes back.
func TestWordsMatchWholeWordsOfText(t *testing.T) {
	w := words{mute: split("AI, sport")}
	for _, e := range []entry{
		{Title: "Minister said rain will fall"},
		{Title: "Weather", Description: `<a href="https://example.org/sport/x">the forecast</a>`},
	} {
		if !w.wants(e) {
			t.Errorf("%q was left out by a word it does not hold", e.Title)
		}
	}
	if w.wants(entry{Title: "New AI rules"}) || w.wants(entry{Title: "x", Description: "<p>Le <b>sport</b> du jour</p>"}) {
		t.Error("an entry holding a mute word was kept")
	}
}

// No words, a trailing comma or a word of punctuation alone leave out nothing:
// the flags unset are what every feed with no words runs with.
func TestNoWordsKeepEveryEntry(t *testing.T) {
	for _, s := range []string{"", "sport,", "!!, ,"} {
		if got := split(s); len(got) > 1 || len(got) == 1 && got[0] != " sport " {
			t.Errorf("split(%q) = %q", s, got)
		}
	}
	if !(words{mute: split(""), only: split("")}).wants(entry{Title: "Hello world"}) {
		t.Error("with no words set, an entry was left out")
	}
	if (words{mute: split("x")}).wants(entry{Title: "a", Description: "<script>var x=1</script><p>b</p>"}) == false {
		t.Error("a word inside a script left the entry out")
	}
}

func TestWordsPickEntries(t *testing.T) {
	items := `<item><title>Football final</title><guid>1</guid>` + long("match") + `</item>` +
		`<item><title>Élections</title><guid>2</guid>` + long("vote") + `</item>` +
		`<item><title>Recette d'été</title><guid>3</guid>` + long("tomates") + `</item>`
	srv := feedOf("News", &items, nil)
	defer srv.Close()
	dir := t.TempDir()
	got, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true, words{mute: split("FOOTBALL")})
	if err != nil || len(got) != 2 {
		t.Fatalf("mute: want 2 entries, got %v, %v", got, err)
	}
	again, err := digest(srv.Client(), dir, srv.URL+"/rss.xml", true)
	if err != nil || len(again) != 0 {
		t.Errorf("a muted entry came back: %v, %v", again, err)
	}
	only, err := digest(srv.Client(), t.TempDir(), srv.URL+"/rss.xml", true, words{only: split(" ete , ")})
	if err != nil || len(only) != 1 || !strings.Contains(only[0], "recette") {
		t.Errorf("only: want the summer recipe alone, got %v, %v", only, err)
	}
}
