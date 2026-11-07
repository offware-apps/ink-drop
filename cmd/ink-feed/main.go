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
	"unicode"

	"codeberg.org/readeck/go-readability/v2"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/unicode/norm"

	"github.com/offware-apps/ink-drop/folder"
	"github.com/offware-apps/ink-drop/internal/stage"
	"github.com/offware-apps/ink-drop/internal/web"
)

const maxFeed = 64 << 20

// maxArticle bounds an entry's page: four parse at once, and a parsed page
// takes many times its size in memory.
const maxArticle = 8 << 20

// An entry whose feed text is shorter than shortText runes is a summary, so its
// page is fetched for the whole article, articlesAtOnce at a time. A run takes
// at most maxArticles new entries of a feed, and tries an article that failed
// for a passing reason in maxTries runs.
const (
	shortText      = 1200
	maxArticles    = 50
	articlesAtOnce = 4
	maxTries       = 3
)

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
	each := flag.Bool("each", false, "write one item per new entry instead of one digest per feed")
	waiting := flag.Bool("pending", false, "print first the items an earlier run left in the folder, for ink-epub -move to take")
	mute := flag.String("mute", "", "comma-separated words: a new entry whose title or text holds one is left out, and counts as seen")
	only := flag.String("only", "", "comma-separated words: a new entry whose title or text holds none is left out, and counts as seen")
	flag.Parse()
	dir, err := stage.Dir(*to, "feed")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ink-feed:", err)
		os.Exit(2)
	}
	if *waiting {
		for _, p := range pending(dir) {
			fmt.Println(p)
		}
	}
	client := &http.Client{Timeout: 60 * time.Second}
	os.Exit(stage.Run("ink-feed", os.Stdin, os.Stdout, func(line string) ([]string, error) {
		return digest(client, dir, line, *each, words{mute: split(*mute), only: split(*only)})
	}))
}

// digest returns the paths of the items it writes in dir with the entries of
// the feed at rawURL not seen on its last run: one digest of them all, or one
// item per entry when each is set, and nothing when no entry is new.
func digest(client *http.Client, dir, rawURL string, each bool, keep ...words) ([]string, error) {
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
		ids = append(ids, e.id())
		if !slices.Contains(seen, e.id()) {
			fresh = append(fresh, e)
		}
	}
	if len(fresh) == 0 {
		return nil, nil
	}
	// The record keeps the feed's entries already seen or written this run, so
	// an entry dropped from the feed leaves it, and one not yet written stays out.
	done := map[string]bool{}
	for _, id := range seen {
		done[id] = true
	}
	record := func() error {
		var keep []string
		for _, id := range ids {
			if done[id] {
				keep = append(keep, id)
			}
		}
		b, _ := json.Marshal(keep)
		if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
			return err
		}
		return folder.WriteAtomic(state, b)
	}
	// Entries the words leave out count as seen, so they never come again.
	if len(keep) > 0 {
		var kept []entry
		for _, e := range fresh {
			if keep[0].wants(e) {
				kept = append(kept, e)
			} else {
				done[e.id()] = true
			}
		}
		if len(kept) < len(fresh) {
			fresh = kept
			if err := record(); err != nil {
				return nil, err
			}
		}
		if len(fresh) == 0 {
			return nil, nil
		}
	}
	// A run takes at most maxArticles new entries, so each gets its whole
	// article; the rest stay unseen and come in the next run.
	if len(fresh) > maxArticles {
		fresh = fresh[:maxArticles]
	}
	// An article that failed for a passing reason, a busy or rate-limiting
	// server, is tried again in the next run, maxTries runs in all, before its
	// entry falls back to the summary.
	triesFile := strings.TrimSuffix(state, ".json") + ".tries.json"
	tries := map[string]int{}
	if b, err := os.ReadFile(triesFile); err == nil {
		json.Unmarshal(b, &tries)
	}
	retry := func(e entry, passing bool) bool {
		if passing && tries[e.id()] < maxTries-1 {
			tries[e.id()]++
			return true
		}
		delete(tries, e.id())
		return false
	}
	saveTries := func() error {
		for id := range tries {
			if !slices.Contains(ids, id) {
				delete(tries, id)
			}
		}
		b, _ := json.Marshal(tries)
		if err := os.MkdirAll(filepath.Dir(triesFile), 0o755); err != nil {
			return err
		}
		return folder.WriteAtomic(triesFile, b)
	}
	fetched := time.Now().UTC().Truncate(time.Second)
	var paths []string
	if each {
		// Pages are written as each group of articles arrives, so a run stopped
		// part way keeps every page it finished.
		for start := 0; start < len(fresh); start += articlesAtOnce {
			group := fresh[start:min(start+articlesAtOnce, len(fresh))]
			pages, passing := articles(client, group, final)
			for i, e := range group {
				if retry(e, passing[i]) {
					continue
				}
				title := first(e.Title, "Untitled")
				source := e.source(final)
				page, err := web.Inline(client, render(title, group[i:i+1], pages[i:i+1], final), "text/html; charset=utf-8", final)
				if err != nil {
					return paths, err
				}
				p, err := folder.Put(dir, folder.Free(dir, stage.Slug(title), ".html", source), page, folder.Meta{Source: source, Title: title, Fetched: fetched, Stage: "ink-feed"})
				if err != nil {
					return paths, err
				}
				paths = append(paths, p)
				done[e.id()] = true
				if err := record(); err != nil {
					return paths, err
				}
			}
			if err := saveTries(); err != nil {
				return paths, err
			}
		}
		return paths, nil
	}
	pages, passing := articles(client, fresh, final)
	var kept []entry
	var keptPages []string
	for i, e := range fresh {
		if !retry(e, passing[i]) {
			kept = append(kept, e)
			keptPages = append(keptPages, pages[i])
		}
	}
	if len(kept) > 0 {
		title := first(f.Channel.Title, f.Title, final.Host) + ", " + fetched.Format("2006-01-02 15:04")
		page, err := web.Inline(client, render(title, kept, keptPages, final), "text/html; charset=utf-8", final)
		if err != nil {
			return nil, err
		}
		// Each digest is its own source: two in one minute, or two feeds of
		// one title, never take each other's place.
		digest := *final
		digest.Fragment = "digest-" + fetched.Format("20060102T150405Z")
		p, err := folder.Put(dir, folder.Free(dir, stage.Slug(title), ".html", digest.String()), page, folder.Meta{Source: digest.String(), Title: title, Fetched: fetched, Stage: "ink-feed"})
		if err != nil {
			return nil, err
		}
		paths = append(paths, p)
		// Recorded only once the digest exists, so a failed run loses no entry.
		for _, e := range kept {
			done[e.id()] = true
		}
		if err := record(); err != nil {
			return paths, err
		}
	}
	return paths, saveTries()
}

// words picks the entries a feed keeps: none holding a mute word, and when
// only is set, each holding one of its words. A word matches a whole word of
// the title or the feed's summary, without case or accents: "ete" finds "Été".
type words struct {
	mute, only []string
}

func (w words) wants(e entry) bool {
	hay := spaced(e.Title + " " + plain(e.html()))
	for _, m := range w.mute {
		if strings.Contains(hay, m) {
			return false
		}
	}
	if len(w.only) == 0 {
		return true
	}
	for _, o := range w.only {
		if strings.Contains(hay, o) {
			return true
		}
	}
	return false
}

// split returns the comma-separated words of s, folded, the empty ones dropped.
func split(s string) []string {
	var out []string
	for _, w := range strings.Split(s, ",") {
		if w = spaced(w); strings.TrimSpace(w) != "" {
			out = append(out, w)
		}
	}
	return out
}

// spaced returns the words of s folded, each between spaces, so a word
// matches only a whole word: "ai" is not found in "rain".
func spaced(s string) string {
	return " " + strings.Join(strings.FieldsFunc(fold(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ") + " "
}

// plain returns the text of an HTML fragment, its tags, addresses, scripts and
// styles left out.
func plain(fragment string) string {
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(fragment))
	hidden := false
	for {
		switch z.Next() {
		case html.ErrorToken:
			return b.String()
		case html.StartTagToken:
			name, _ := z.TagName()
			hidden = string(name) == "script" || string(name) == "style"
		case html.EndTagToken:
			hidden = false
		case html.TextToken:
			if !hidden {
				b.Write(z.Text())
				b.WriteByte(' ')
			}
		}
	}
}

// fold lowers s and drops its accents.
func fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// pending returns the items waiting in dir, each with its sidecar: what an
// earlier run wrote and no ink-epub -move has taken yet.
func pending(dir string) []string {
	var out []string
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		if f.Type().IsRegular() && strings.HasSuffix(f.Name(), ".html") {
			if _, err := folder.Get(filepath.Join(dir, f.Name())); err == nil {
				out = append(out, filepath.Join(dir, f.Name()))
			}
		}
	}
	return out
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

// render returns the digest page: a contents list linking to each entry, then
// per entry its title, its date and its article where one was fetched, else
// the feed's text, every relative address made absolute against its link.
func render(title string, entries []entry, pages []string, feedURL *url.URL) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>%s</title></head><body>`, html.EscapeString(title))
	if len(entries) > 1 {
		b.WriteString("<ol>")
		for i, e := range entries {
			fmt.Fprintf(&b, `<li><a href="#entry-%d">%s</a></li>`, i+1, html.EscapeString(first(e.Title, "Untitled")))
		}
		b.WriteString("</ol>")
	}
	for i, e := range entries {
		base, err := feedURL.Parse(e.link())
		if err != nil {
			base = feedURL
		}
		// A lone entry's book already carries its title in its table of contents.
		if len(entries) > 1 {
			fmt.Fprintf(&b, `<h2 id="entry-%d">%s</h2>`, i+1, html.EscapeString(first(e.Title, "Untitled")))
		} else {
			fmt.Fprintf(&b, `<h2>%s</h2>`, html.EscapeString(first(e.Title, "Untitled")))
		}
		if d := first(e.PubDate, e.Published, e.Updated); d != "" {
			fmt.Fprintf(&b, "<p>%s</p>", html.EscapeString(d))
		}
		body := pages[i]
		if body == "" {
			body = e.html()
		}
		nodes, _ := html.ParseFragment(strings.NewReader(body), &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body})
		for _, n := range nodes {
			own(n)
			absolute(n, base)
			html.Render(&b, n)
		}
		fmt.Fprintf(&b, `<p><a href="%s">%s</a></p>`, html.EscapeString(base.String()), html.EscapeString(base.Host))
	}
	b.WriteString("</body></html>")
	return b.Bytes()
}

// articles returns, per entry, the article on its page when the feed carries
// only a summary of it, else "", and whether its fetch failed for a passing
// reason. A page holding less text than the summary, a consent or a login
// wall for one, falls back to the summary too.
func articles(client *http.Client, entries []entry, feedURL *url.URL) ([]string, []bool) {
	pages := make([]string, len(entries))
	passing := make([]bool, len(entries))
	slots := make(chan struct{}, articlesAtOnce)
	done := make(chan struct{})
	started := 0
	for i, e := range entries {
		// An entry with no link of its own would resolve to the feed itself.
		if e.link() == "" || textLen(e.html()) >= shortText {
			continue
		}
		link, err := feedURL.Parse(e.link())
		if err != nil || (link.Scheme != "http" && link.Scheme != "https") {
			continue
		}
		started++
		go func() {
			slots <- struct{}{}
			page, err := article(client, link)
			if err == nil && textLen(page) > textLen(e.html()) {
				pages[i] = page
			}
			passing[i] = err != nil && web.Passing(err)
			<-slots
			done <- struct{}{}
		}()
	}
	for range started {
		<-done
	}
	return pages, passing
}

// article returns the main text of the page at link, as HTML.
func article(client *http.Client, link *url.URL) (string, error) {
	body, ctype, final, err := web.Get(client, link.String(), maxArticle)
	if err != nil {
		return "", err
	}
	if ctype != "text/html" {
		return "", errors.New("not an HTML page")
	}
	a, err := readability.FromReader(bytes.NewReader(body), final)
	if err != nil || a.Node == nil {
		return "", errors.New("no article found")
	}
	var b bytes.Buffer
	if err := a.RenderHTML(&b); err != nil {
		return "", err
	}
	return b.String(), nil
}

// textLen returns the number of runes of text in the HTML fragment s.
func textLen(s string) int {
	nodes, _ := html.ParseFragment(strings.NewReader(s), &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body})
	n := 0
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			n += len([]rune(strings.TrimSpace(x.Data)))
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, x := range nodes {
		walk(x)
	}
	return n
}

// own renames an article's ids opening on "entry-", and the links to them, so
// none takes the place of the digest's own entry anchors.
func own(n *html.Node) {
	for i, a := range n.Attr {
		if a.Key == "id" && strings.HasPrefix(a.Val, "entry-") {
			n.Attr[i].Val = "in-" + a.Val
		}
		if a.Key == "href" && strings.HasPrefix(a.Val, "#entry-") {
			n.Attr[i].Val = "#in-" + a.Val[1:]
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		own(c)
	}
}

// absolute rewrites every href and src under n against base, leaving a link
// within the page, a footnote's, to the anchor it names in the book.
func absolute(n *html.Node, base *url.URL) {
	for i, a := range n.Attr {
		if (a.Key == "href" || a.Key == "src") && !strings.HasPrefix(a.Val, "#") {
			if u, err := base.Parse(a.Val); err == nil {
				n.Attr[i].Val = u.String()
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		absolute(c, base)
	}
}

// id returns what names the entry from one run to the next: its guid or
// Atom id, else its link and its title, since entries with no id may all link
// to one page.
func (e entry) id() string {
	if id := first(e.GUID, e.ID); id != "" {
		return id
	}
	if e.link() != "" && strings.TrimSpace(e.Title) != "" {
		return e.link() + "#" + strings.TrimSpace(e.Title)
	}
	return first(e.link(), e.Title)
}

// source returns the address that names the entry alone: its link, else the
// feed's, with a fragment wherever the link does not name it by itself, so no
// two entries are ever taken for one page.
func (e entry) source(feed *url.URL) string {
	u := *feed
	parsed := false
	if e.link() != "" {
		if link, err := feed.Parse(e.link()); err == nil {
			u, parsed = *link, true
		}
	}
	key := first(e.GUID, e.ID)
	switch {
	case !parsed:
		u.Fragment = e.id()
	case key == "":
		u.Fragment = strings.TrimSpace(e.Title)
	case key != e.link() && key != u.String():
		u.Fragment = key
	}
	return u.String()
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
