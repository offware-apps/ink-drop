// Command ink-feed turns each RSS or Atom feed read from stdin into one digest
// item holding the entries that are new since its last run.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/html/charset"

	"github.com/offware-apps/ink-drop/folder"
	"github.com/offware-apps/ink-drop/internal/stage"
	"github.com/offware-apps/ink-drop/internal/web"
)

const maxFeed = 64 << 20

// feed holds the fields ink-feed reads from an RSS 2.0 or an Atom document.
type feed struct {
	XMLName xml.Name
	Title   string `xml:"title"`
	Channel struct {
		Title string  `xml:"title"`
		Items []entry `xml:"item"`
	} `xml:"channel"`
	Entries []entry `xml:"entry"`
}

// entry is an RSS item or an Atom entry.
type entry struct {
	Title       string `xml:"title"`
	Links       []link `xml:"link"`
	GUID        string `xml:"guid"`
	ID          string `xml:"id"`
	PubDate     string `xml:"pubDate"`
	Published   string `xml:"published"`
	Updated     string `xml:"updated"`
	Encoded     string `xml:"encoded"`
	Description string `xml:"description"`
	Content     text   `xml:"http://www.w3.org/2005/Atom content"`
	Summary     text   `xml:"http://www.w3.org/2005/Atom summary"`
}

// link is an RSS link, its URL as text, or an Atom one, its URL in href.
type link struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
	URL  string `xml:",chardata"`
}

// text is an Atom content or summary element.
type text struct {
	Type  string `xml:"type,attr"`
	Body  string `xml:",chardata"`
	Inner string `xml:",innerxml"`
}

func main() {
	to := flag.String("to", "", "output folder (default: the user cache folder)")
	flag.Parse()
	dir, err := stage.Dir(*to, "feed")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ink-feed:", err)
		os.Exit(2)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	os.Exit(stage.Run("ink-feed", os.Stdin, os.Stdout, func(line string) ([]string, error) {
		return digest(client, dir, line)
	}))
}

// digest returns the path of one item it writes in dir with the entries of the
// feed at rawURL not seen on its last run, or nothing when no entry is new.
func digest(client *http.Client, dir, rawURL string) ([]string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("not an http or https URL")
	}
	body, ctype, final, err := web.Get(client, u.String(), maxFeed)
	if err == nil && ctype == "text/html" {
		body, final, err = discover(client, body, final)
	}
	if err != nil {
		return nil, err
	}
	var f feed
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.CharsetReader = charset.NewReaderLabel
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	if f.XMLName.Local != "rss" && f.XMLName.Local != "feed" {
		return nil, errors.New("not an RSS 2.0 or Atom feed")
	}
	// A hash, since a slug folds case and stops at 80 bytes, which merges feeds.
	state := filepath.Join(dir, folder.SidecarDir, "feeds", fmt.Sprintf("%x.json", sha256.Sum256([]byte(u.String()))))
	var seen []string
	b, err := os.ReadFile(state)
	if err == nil {
		err = json.Unmarshal(b, &seen)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %v", state, err)
	}
	var ids []string
	var fresh []entry
	for _, e := range append(f.Channel.Items, f.Entries...) {
		id := first(e.GUID, e.ID, e.link(), e.Title)
		ids = append(ids, id)
		if !slices.Contains(seen, id) {
			fresh = append(fresh, e)
		}
	}
	if len(fresh) == 0 {
		return nil, nil
	}
	fetched := time.Now().UTC().Truncate(time.Second)
	title := first(f.Channel.Title, f.Title, final.Host) + ", " + fetched.Format("2006-01-02 15:04")
	page, err := web.Inline(client, render(title, fresh, final), "text/html; charset=utf-8", final)
	if err != nil {
		return nil, err
	}
	p, err := folder.Put(dir, stage.Slug(title)+".html", page, folder.Meta{Source: final.String(), Title: title, Fetched: fetched, Stage: "ink-feed"})
	if err != nil {
		return nil, err
	}
	// Saved only once the digest exists, so a failed run loses no entry.
	b, _ = json.Marshal(ids)
	if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
		return nil, err
	}
	return []string{p}, folder.WriteAtomic(state, b)
}

// discover returns the first feed a site's page names in a link tag, so the
// user can hand ink-feed the address of a site rather than of its feed.
func discover(client *http.Client, page []byte, base *url.URL) ([]byte, *url.URL, error) {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return nil, nil, err
	}
	var href string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Link && href == "" {
			var rel, typ, h string
			for _, a := range n.Attr {
				switch a.Key {
				case "rel":
					rel = a.Val
				case "type":
					typ = a.Val
				case "href":
					h = a.Val
				}
			}
			if slices.Contains(strings.Fields(strings.ToLower(rel)), "alternate") && (typ == "application/rss+xml" || typ == "application/atom+xml") {
				href = h
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if href == "" {
		return nil, nil, errors.New("not a feed, and the page links to none")
	}
	u, err := base.Parse(href)
	if err != nil {
		return nil, nil, err
	}
	body, _, final, err := web.Get(client, u.String(), maxFeed)
	return body, final, err
}

// render returns the digest page: per entry its linked title, its date and its
// content, every relative address made absolute against the entry's link.
func render(title string, entries []entry, feedURL *url.URL) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>%s</title></head><body>`, html.EscapeString(title))
	for _, e := range entries {
		base, err := feedURL.Parse(e.link())
		if err != nil {
			base = feedURL
		}
		fmt.Fprintf(&b, `<h2><a href="%s">%s</a></h2>`, html.EscapeString(base.String()), html.EscapeString(e.Title))
		if d := first(e.PubDate, e.Published, e.Updated); d != "" {
			fmt.Fprintf(&b, "<p>%s</p>", html.EscapeString(d))
		}
		nodes, _ := html.ParseFragment(strings.NewReader(e.html()), &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body})
		for _, n := range nodes {
			absolute(n, base)
			html.Render(&b, n)
		}
	}
	b.WriteString("</body></html>")
	return b.Bytes()
}

// absolute rewrites every href and src under n against base.
func absolute(n *html.Node, base *url.URL) {
	for i, a := range n.Attr {
		if a.Key == "href" || a.Key == "src" {
			if u, err := base.Parse(a.Val); err == nil {
				n.Attr[i].Val = u.String()
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		absolute(c, base)
	}
}

// link returns the entry's own page: an RSS link, or an Atom alternate one.
func (e entry) link() string {
	for _, l := range e.Links {
		if l.Rel == "" || l.Rel == "alternate" {
			return first(l.URL, l.Href)
		}
	}
	return ""
}

// html returns the entry's full content as HTML, else its summary.
func (e entry) html() string {
	return first(e.Encoded, e.Content.html(), e.Description, e.Summary.html())
}

// html returns t as HTML. Atom text without a type is plain text, per RFC
// 4287, so it is escaped.
func (t text) html() string {
	switch t.Type {
	case "html":
		return t.Body
	case "xhtml":
		return t.Inner
	}
	return html.EscapeString(t.Body)
}

// first returns the first of ss that is not blank, trimmed.
func first(ss ...string) string {
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}
