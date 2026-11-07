// Package web downloads what a stage fetches and inlines a page's images, so
// every later stage stays offline.
package web

import (
	"bytes"
	"encoding/base64"
	"errors"
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
		return nil, "", nil, StatusError{resp.StatusCode, resp.Status}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", nil, err
	}
	if int64(len(body)) > limit {
		return nil, "", nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, limit)
	}
	ctype, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ctype == "" {
		ctype, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}
	return body, ctype, resp.Request.URL, nil
}

// ErrTooLarge is a body over the limit Get was given.
var ErrTooLarge = errors.New("larger than the limit")

// StatusError is a reply other than 200 OK.
type StatusError struct {
	Code   int
	Status string
}

func (e StatusError) Error() string { return "HTTP " + e.Status }

// Passing reports whether err may not happen again: no reply at all, a
// timeout or a reset for one, or a server busy or rate-limiting, 429 or 5xx.
// A page gone, 404 for one, is not passing.
func Passing(err error) bool {
	var s StatusError
	if errors.As(err, &s) {
		return s.Code == http.StatusTooManyRequests || s.Code >= 500
	}
	var u *url.Error
	return errors.As(err, &u)
}

// maxImages bounds the bytes read for one page's images in all, an image
// refused for its type or its size included, since a page of many large
// photographs takes many times its size in memory on its way to a book. A body
// the server cuts short is not counted, Get keeping none of it. An image past
// the budget keeps its remote address, which ink-epub drops. A digest is one
// page: its articles share the budget.
const maxImages = 32 << 20

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
	budget := int64(maxImages)
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			if c.Type == html.ElementNode && c.Data == "source" && n.Data == "picture" {
				n.RemoveChild(c)
			} else {
				if c.Type == html.ElementNode && c.Data == "img" {
					inlineImage(client, c, base, seen, &budget)
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
func inlineImage(client *http.Client, img *html.Node, base *url.URL, seen map[string]string, budget *int64) {
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
		var body []byte
		var ctype string
		err := errors.New("the page's images reached their budget")
		if *budget > 0 {
			limit := min(maxImage, *budget)
			body, ctype, _, err = Get(client, u.String(), limit)
			// Every byte read counts, an image refused for its type or its
			// size included; one too big for what is left ends the budget.
			*budget -= int64(len(body))
			if errors.Is(err, ErrTooLarge) {
				*budget -= limit
			}
		}
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
