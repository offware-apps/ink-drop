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
<!-- note --></body></html>`

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
	for _, gone := range []string{"<script", "alert", "<iframe", "<form", "<input", "onclick", "style=", "o:p", "remote.png", "note", "\x0c"} {
		if strings.Contains(text, gone) {
			t.Errorf("text.xhtml still holds %q", gone)
		}
	}
	for _, kept := range []string{"Été", "<b>gras</b>", `href="https://example.org/x"`, "gardé", "<br/>", `src="images/img-001.png"`, `xml:lang="fr"`, "Un &lt;titre&gt; &amp; co"} {
		if !strings.Contains(text, kept) {
			t.Errorf("text.xhtml lost %q:\n%s", kept, text)
		}
	}
	if _, ok := files["OEBPS/images/img-001.png"]; !ok || len(z.File) != 6 {
		t.Errorf("want one deduplicated image among 6 entries, got %d", len(z.File))
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
