package main

import (
	"net/http"
	"net/http/httptest"
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
		w.Write([]byte(`<html><body><p>Not a feed.</p></body></html>`))
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
	paths, err := digest(srv.Client(), t.TempDir(), srv.URL+"/rss.xml")
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
	if err != nil || m.Source != srv.URL+"/rss.xml" || m.Stage != "ink-feed" || !strings.HasPrefix(m.Title, "A blog, ") {
		t.Errorf("sidecar %+v, %v", m, err)
	}
}

func TestFeedAtom(t *testing.T) {
	srv := server(nil)
	defer srv.Close()
	paths, err := digest(srv.Client(), t.TempDir(), srv.URL+"/atom.xml")
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
		paths, err := digest(srv.Client(), dir, srv.URL+"/rss.xml")
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
		paths, err := digest(srv.Client(), dir, srv.URL+"/rss.xml"+q)
		if err != nil {
			t.Fatal(err)
		}
		read(t, paths)
	}
}

func TestFeedRejectsPage(t *testing.T) {
	srv := server(nil)
	defer srv.Close()
	if _, err := digest(srv.Client(), t.TempDir(), srv.URL+"/page.html"); err == nil {
		t.Error("a page that is not a feed passed as an empty one")
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
	if _, err := digest(srv.Client(), dir, srv.URL+"/rss.xml"); err == nil {
		t.Fatal("a digest landed in a read-only folder")
	}
	os.Chmod(dir, 0o755)
	paths, err := digest(srv.Client(), dir, srv.URL+"/rss.xml")
	if err != nil {
		t.Fatal(err)
	}
	read(t, paths)
}
