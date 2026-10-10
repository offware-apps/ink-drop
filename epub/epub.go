// Package epub packs one HTML article into an EPUB 3 file any reader opens.
package epub

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Book is what Build needs besides the article itself.
type Book struct {
	Title    string
	Source   string
	Modified time.Time
}

type image struct {
	name, mediaType string
	data            []byte
}

// Build returns the EPUB of the HTML document doc. It keeps the text, the
// links and the images held as data URIs, and drops scripts, forms, embedded
// frames and every image still on the network.
func Build(b Book, doc io.Reader) ([]byte, error) {
	root, err := html.Parse(doc)
	if err != nil {
		return nil, err
	}
	lang := "und"
	var body *html.Node
	var find func(n *html.Node)
	find = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Html && attr(n, "lang") != "" {
			lang = attr(n, "lang")
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Body {
			body = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(root)
	if body == nil {
		return nil, fmt.Errorf("epub: document has no body")
	}
	var images []image
	byData := map[string]string{}
	clean(body, func(img *html.Node) bool {
		mt, data, ok := decodeImage(attr(img, "src"))
		if !ok {
			return false
		}
		name, seen := byData[string(data)]
		if !seen {
			name = fmt.Sprintf("images/img-%03d.%s", len(images)+1, strings.TrimPrefix(mt, "image/"))
			byData[string(data)] = name
			images = append(images, image{name, mt, data})
		}
		setAttr(img, "src", name)
		return true
	})
	var text bytes.Buffer
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&text, c); err != nil {
			return nil, err
		}
	}
	return pack(b, lang, text.String(), images)
}

// dropped elements lose their whole subtree: each runs code, takes input,
// loads from the network or holds raw text that XHTML cannot carry.
var dropped = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Iframe: true,
	atom.Object: true, atom.Embed: true, atom.Form: true, atom.Input: true,
	atom.Button: true, atom.Select: true, atom.Textarea: true, atom.Svg: true,
	atom.Math: true, atom.Canvas: true, atom.Video: true, atom.Audio: true,
	atom.Link: true, atom.Meta: true, atom.Base: true, atom.Template: true,
	atom.Frame: true, atom.Frameset: true, atom.Noembed: true, atom.Noframes: true,
	atom.Xmp: true, atom.Plaintext: true,
}

var keptAttrs = map[string]bool{"href": true, "src": true, "alt": true, "title": true, "colspan": true, "rowspan": true, "lang": true}

// clean strips n in place down to markup valid as XHTML. keepImg rewrites an
// img and reports whether to keep it.
func clean(n *html.Node, keepImg func(*html.Node) bool) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		switch {
		case c.Type == html.CommentNode, c.Type == html.ElementNode && dropped[c.DataAtom]:
			n.RemoveChild(c)
		case c.Type == html.TextNode:
			c.Data = xmlChars(c.Data)
		case c.Type == html.ElementNode && c.DataAtom == 0:
			// An element HTML does not define, o:p from a word processor for
			// one, may not be a valid XML name: keep its children alone.
			if c.FirstChild != nil {
				next = c.FirstChild
			}
			for gc := c.FirstChild; gc != nil; gc = c.FirstChild {
				c.RemoveChild(gc)
				n.InsertBefore(gc, c)
			}
			n.RemoveChild(c)
		case c.Type == html.ElementNode:
			keep := c.Attr[:0]
			for _, a := range c.Attr {
				if a.Namespace == "" && keptAttrs[a.Key] && (a.Key != "href" || safeHref(a.Val)) {
					a.Val = xmlChars(a.Val)
					keep = append(keep, a)
				}
			}
			c.Attr = keep
			if c.DataAtom == atom.Img && !keepImg(c) {
				n.RemoveChild(c)
				break
			}
			clean(c, keepImg)
		}
		c = next
	}
}

func safeHref(v string) bool {
	if strings.HasPrefix(v, "#") {
		return true
	}
	u, err := url.Parse(v)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "mailto")
}

// xmlChars drops the control characters XML 1.0 forbids, which HTML allows.
func xmlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0xFFFE || r == 0xFFFF {
			return -1
		}
		return r
	}, s)
}

var imageTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}

func decodeImage(src string) (string, []byte, bool) {
	rest, ok := strings.CutPrefix(src, "data:")
	if !ok {
		return "", nil, false
	}
	head, payload, ok := strings.Cut(rest, ",")
	mt, enc, _ := strings.Cut(head, ";")
	if !ok || enc != "base64" || !imageTypes[mt] {
		return "", nil, false
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(data) == 0 {
		return "", nil, false
	}
	return mt, data, true
}

func pack(b Book, lang, text string, images []image) ([]byte, error) {
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	// The mimetype entry comes first and uncompressed, with no data descriptor,
	// so a reader can identify the file from its first bytes.
	mimetype := []byte("application/epub+zip")
	w, err := z.CreateRaw(&zip.FileHeader{
		Name: "mimetype", Method: zip.Store, CRC32: crc32.ChecksumIEEE(mimetype),
		CompressedSize64: uint64(len(mimetype)), UncompressedSize64: uint64(len(mimetype)),
	})
	if err != nil {
		return nil, err
	}
	w.Write(mimetype)
	title, src, l := esc(b.Title), esc(b.Source), esc(lang)
	files := []struct {
		name string
		data []byte
	}{
		{"META-INF/container.xml", []byte(container)},
		{"OEBPS/content.opf", []byte(opf(b, title, src, l, images))},
		{"OEBPS/nav.xhtml", []byte(fmt.Sprintf(nav, l, l, title, title))},
		{"OEBPS/text.xhtml", []byte(fmt.Sprintf(page, l, l, title, title, src, esc(host(b.Source)), text))},
	}
	for _, img := range images {
		files = append(files, struct {
			name string
			data []byte
		}{"OEBPS/" + img.name, img.data})
	}
	for _, f := range files {
		w, err := z.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func opf(b Book, title, src, lang string, images []image) string {
	var items strings.Builder
	for i, img := range images {
		fmt.Fprintf(&items, "\n    <item id=\"img%d\" href=\"%s\" media-type=\"%s\"/>", i+1, img.name, img.mediaType)
	}
	return fmt.Sprintf(opfTmpl, lang, id(b.Source), title, lang, src, b.Modified.UTC().Format(time.RFC3339), items.String())
}

// id returns a stable urn:uuid for source, so fetching the same page again
// replaces the book in a reader's library rather than duplicating it.
func id(source string) string {
	h := sha1.Sum([]byte("ink-drop:" + source))
	h[6] = h[6]&0x0f | 0x50
	h[8] = h[8]&0x3f | 0x80
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

func host(source string) string {
	if u, err := url.Parse(source); err == nil && u.Host != "" {
		return u.Host
	}
	return source
}

func esc(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(xmlChars(s)))
	return b.String()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func setAttr(n *html.Node, key, val string) {
	for i := range n.Attr {
		if n.Attr[i].Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

const container = `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`

const opfTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id" xml:lang="%s">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="id">%s</dc:identifier>
    <dc:title>%s</dc:title>
    <dc:language>%s</dc:language>
    <dc:source>%s</dc:source>
    <meta property="dcterms:modified">%s</meta>
  </metadata>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="text" href="text.xhtml" media-type="application/xhtml+xml"/>%s
  </manifest>
  <spine>
    <itemref idref="text"/>
  </spine>
</package>
`

const nav = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="%s" lang="%s">
<head><meta charset="utf-8"/><title>%s</title></head>
<body><nav epub:type="toc"><ol><li><a href="text.xhtml">%s</a></li></ol></nav></body>
</html>
`

const page = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="%s" lang="%s">
<head><meta charset="utf-8"/><title>%s</title></head>
<body>
<h1>%s</h1>
<p><a href="%s">%s</a></p>
%s
</body>
</html>
`
