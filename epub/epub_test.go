package epub

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"
)

const pixel = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

const doc = `<!DOCTYPE html><html lang="fr"><head><title>x</title></head><body>
<p onclick="alert(1)" style="color:red">Été&nbsp;<b>gras</b> & <a href="javascript:alert(1)">lien</a> <a href="https://example.org/x">ok</a>` + "\x0c" + `</p>
<script>alert(1)</script><iframe src="https://example.org"></iframe><form><input></form>
<o:p>gardé</o:p><br>
<img src="` + pixel + `" alt="un"><img src="` + pixel + `" alt="deux"><img src="https://example.org/remote.png">
<p id="note-1">une note</p><p id="note-1">doublon</p><p id="1bad">x</p><p id="Réf">y</p>
<a href="#note-1">vers</a> <a href="#missing">perdu</a> <a href="#R%C3%A9f">réf</a>
<pre>if x {
` + "\t" + `<span>y</span>
}</pre><p>tab` + "\t" + `here</p>
<!-- commentaire --></body></html>`

func TestBuild(t *testing.T) {
	b, err := Build(Book{Title: "Un <titre> & co", Source: "https://example.org/post?a=1&b=2", Modified: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)}, strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if f := z.File[0]; f.Name != "mimetype" || f.Method != zip.Store || f.Flags&0x8 != 0 || !bytes.HasPrefix(b[30:], []byte("mimetypeapplication/epub+zip")) {
		t.Fatalf("first entry %q, method %d, flags %x", f.Name, f.Method, f.Flags)
	}
	files := map[string]string{}
	for _, f := range z.File {
		r, _ := f.Open()
		data, _ := io.ReadAll(r)
		files[f.Name] = string(data)
		if strings.HasSuffix(f.Name, ".xhtml") || strings.HasSuffix(f.Name, ".opf") || strings.HasSuffix(f.Name, ".xml") {
			d := xml.NewDecoder(bytes.NewReader(data))
			d.Strict = true
			for {
				if _, err := d.Token(); err == io.EOF {
					break
				} else if err != nil {
					t.Fatalf("%s is not well-formed XML: %v\n%s", f.Name, err, data)
				}
			}
		}
	}
	text := files["OEBPS/text.xhtml"]
	for _, gone := range []string{"<script", "alert", "<iframe", "<form", "<input", "onclick", "style=", "o:p", "remote.png", "commentaire", "\x0c"} {
		if strings.Contains(text, gone) {
			t.Errorf("text.xhtml still holds %q", gone)
		}
	}
	for _, kept := range []string{"Été", "<b>gras</b>", `href="https://example.org/x"`, "gardé", "<br/>", `src="images/img-001.png"`, `xml:lang="fr"`, "Un &lt;titre&gt; &amp; co"} {
		if !strings.Contains(text, kept) {
			t.Errorf("text.xhtml lost %q:\n%s", kept, text)
		}
	}
	if strings.Count(text, `id="note-1"`) != 1 || strings.Contains(text, `id="1bad"`) || !strings.Contains(text, `<a href="#note-1">`) || !strings.Contains(text, "<a>perdu</a>") || !strings.Contains(text, `<a href="#Réf">`) {
		t.Errorf("ids or in-page links not repaired:\n%s", text)
	}
	if _, ok := files["OEBPS/images/img-001.png"]; !ok || len(z.File) != 7 {
		t.Errorf("want one deduplicated image among 7 entries, got %d", len(z.File))
	}
	// A reader applies a stylesheet only if the page links it and the manifest lists it.
	if !strings.Contains(text, "if x {\n  <span>y</span>\n}") || !strings.Contains(text, "tab\there") {
		t.Errorf("tabs under pre are not spaces, or tabs elsewhere changed:\n%s", text)
	}
	if !strings.Contains(files["OEBPS/style.css"], "code") || !strings.Contains(files["OEBPS/style.css"], `"Droid Sans Mono", monospace`) ||
		!strings.Contains(text, `<link rel="stylesheet" href="style.css"/>`) ||
		!strings.Contains(files["OEBPS/content.opf"], `href="style.css" media-type="text/css"`) {
		t.Error("code has no monospace stylesheet reaching the page")
	}
	if !strings.Contains(files["OEBPS/content.opf"], `href="images/img-001.png" media-type="image/png"`) {
		t.Errorf("image missing from the manifest:\n%s", files["OEBPS/content.opf"])
	}
}

func TestIDStable(t *testing.T) {
	a, b := id("https://example.org/a"), id("https://example.org/a")
	if a != b || a == id("https://example.org/b") || len(a) != len("urn:uuid:")+36 {
		t.Fatalf("ids %s %s", a, b)
	}
}

// A source link appears only where it is an address a reader can follow.
func TestSourceLink(t *testing.T) {
	for source, want := range map[string]bool{
		"https://example.org/a": true, "": false, "javascript:alert(1)": false, " javascript:alert(1)": false,
		"file:///etc/passwd": false, "data:text/html,x": false, "#top": false,
	} {
		b, err := Build(Book{Title: "t", Source: source}, strings.NewReader("<p>x</p>"))
		if err != nil {
			t.Fatal(err)
		}
		z, _ := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		for _, f := range z.File {
			if f.Name != "OEBPS/text.xhtml" {
				continue
			}
			r, _ := f.Open()
			text, _ := io.ReadAll(r)
			if got := strings.Contains(string(text), "<a href"); got != want {
				t.Errorf("source %q: link %v", source, got)
			}
		}
	}
}

func TestNavListsChapters(t *testing.T) {
	b, err := Build(Book{Title: "News"}, strings.NewReader(`<h2 id="entry-1">First <em>one</em></h2><p>x</p><h2>No id</h2><h2 id="entry-2">Second</h2>`))
	if err != nil {
		t.Fatal(err)
	}
	z, _ := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	for _, f := range z.File {
		if f.Name != "OEBPS/nav.xhtml" {
			continue
		}
		r, _ := f.Open()
		nav, _ := io.ReadAll(r)
		want := `<a href="text.xhtml">News</a><ol><li><a href="text.xhtml#entry-1">First one</a></li><li><a href="text.xhtml#entry-2">Second</a></li></ol>`
		if !strings.Contains(string(nav), want) {
			t.Errorf("nav %s, want %s", nav, want)
		}
		return
	}
	t.Fatal("no nav.xhtml")
}

// In a digest, an article's own headings stay out of the contents.
func TestNavListsOnlyEntriesOfADigest(t *testing.T) {
	b, err := Build(Book{Title: "News"}, strings.NewReader(`<h2 id="entry-1">First</h2><h2 id="background">Background</h2><h2 id="entry-2">Second</h2>`))
	if err != nil {
		t.Fatal(err)
	}
	z, _ := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	for _, f := range z.File {
		if f.Name == "OEBPS/nav.xhtml" {
			r, _ := f.Open()
			nav, _ := io.ReadAll(r)
			if strings.Contains(string(nav), "Background") || !strings.Contains(string(nav), "#entry-2") {
				t.Errorf("nav %s", nav)
			}
			return
		}
	}
	t.Fatal("no nav.xhtml")
}
