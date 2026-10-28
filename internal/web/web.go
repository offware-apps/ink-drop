// Package web downloads what a stage fetches and inlines a page's images, so
// every later stage stays offline.
package web

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const maxImage = 10 << 20

// Get returns the body, its media type and the URL it came from after
// redirects, refusing a body over limit bytes.
func Get(client *http.Client, rawURL string, limit int64) ([]byte, string, *url.URL, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", nil, err
	}
	req.Header.Set("User-Agent", "ink-drop")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", nil, err
	}
	if int64(len(body)) > limit {
		return nil, "", nil, fmt.Errorf("larger than %d bytes", limit)
	}
	ctype, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ctype == "" {
		ctype, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}
	return body, ctype, resp.Request.URL, nil
}

// Inline returns the page as UTF-8 with every image it can download turned
// into a data URI. An image that fails keeps its remote address.
func Inline(client *http.Client, body []byte, ctype string, base *url.URL) ([]byte, error) {
	r, err := charset.NewReader(bytes.NewReader(body), ctype)
	if err != nil {
		return nil, err
	}
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}
	seen := map[string]string{}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			if c.Type == html.ElementNode && c.Data == "source" && n.Data == "picture" {
				n.RemoveChild(c)
			} else {
				if c.Type == html.ElementNode && c.Data == "img" {
					inlineImage(client, c, base, seen)
				}
				walk(c)
			}
			c = next
		}
	}
	walk(doc)
	var out bytes.Buffer
	err = html.Render(&out, doc)
	return out.Bytes(), err
}

// inlineImage points img at a data URI and drops the lazy-loading attributes,
// which would otherwise send a later stage back to the network.
func inlineImage(client *http.Client, img *html.Node, base *url.URL, seen map[string]string) {
	src := attr(img, "src")
	for _, k := range []string{"data-src", "data-original", "data-lazy-src"} {
		if v := attr(img, k); v != "" && (src == "" || strings.HasPrefix(src, "data:")) {
			src = v
		}
	}
	keep := img.Attr[:0]
	for _, a := range img.Attr {
		if a.Key != "srcset" && a.Key != "sizes" && a.Key != "loading" && a.Key != "class" && !strings.HasPrefix(a.Key, "data-") && a.Key != "src" {
			keep = append(keep, a)
		}
	}
	img.Attr = keep
	if strings.HasPrefix(src, "data:") || src == "" {
		img.Attr = append(img.Attr, html.Attribute{Key: "src", Val: src})
		return
	}
	u, err := base.Parse(src)
	if err != nil {
		img.Attr = append(img.Attr, html.Attribute{Key: "src", Val: src})
		return
	}
	data, ok := seen[u.String()]
	if !ok {
		body, ctype, _, err := Get(client, u.String(), maxImage)
		if err == nil && !strings.HasPrefix(ctype, "image/") {
			err = fmt.Errorf("content type %q", ctype)
		}
		if err == nil {
			data = "data:" + ctype + ";base64," + base64.StdEncoding.EncodeToString(body)
		} else {
			fmt.Fprintf(os.Stderr, "%s: image %s skipped: %v\n", filepath.Base(os.Args[0]), u, err)
			data = u.String()
		}
		seen[u.String()] = data
	}
	img.Attr = append(img.Attr, html.Attribute{Key: "src", Val: data})
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
