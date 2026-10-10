// Command ink-fetch downloads each URL read from stdin into the work folder: a
// PDF as it is, an HTML page with its images inlined so later stages stay
// offline.
package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"github.com/offware-apps/ink-drop/folder"
	"github.com/offware-apps/ink-drop/internal/stage"
)

const (
	maxPage  = 64 << 20
	maxImage = 10 << 20
)

func main() {
	to := flag.String("to", "", "output folder (default: the user cache folder)")
	flag.Parse()
	dir, err := stage.Dir(*to, "fetch")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ink-fetch:", err)
		os.Exit(2)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	os.Exit(stage.Run("ink-fetch", os.Stdin, os.Stdout, func(line string) ([]string, error) {
		p, err := fetch(client, dir, line)
		return []string{p}, err
	}))
}

// fetch saves the document at rawURL into dir and returns its path.
func fetch(client *http.Client, dir, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("not an http or https URL")
	}
	body, ctype, final, err := get(client, u.String(), maxPage)
	if err != nil {
		return "", err
	}
	stem := stage.Slug(final.Host + strings.TrimSuffix(final.Path, path.Ext(final.Path)))
	meta := folder.Meta{Source: final.String(), Fetched: time.Now().UTC().Truncate(time.Second), Stage: "ink-fetch"}
	switch {
	case ctype == "application/pdf" || bytes.HasPrefix(body, []byte("%PDF-")):
		return folder.Put(dir, stem+".pdf", body, meta)
	case ctype == "text/html" || ctype == "application/xhtml+xml":
		page, err := inline(client, body, ctype, final)
		if err != nil {
			return "", err
		}
		return folder.Put(dir, stem+".html", page, meta)
	}
	return "", fmt.Errorf("unsupported content type %q", ctype)
}

// get returns the body, its media type and the URL it came from after
// redirects, refusing a body over limit bytes.
func get(client *http.Client, rawURL string, limit int64) ([]byte, string, *url.URL, error) {
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

// inline returns the page as UTF-8 with every image it can download turned
// into a data URI. An image that fails keeps its remote address.
func inline(client *http.Client, body []byte, ctype string, base *url.URL) ([]byte, error) {
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
		body, ctype, _, err := get(client, u.String(), maxImage)
		if err == nil && !strings.HasPrefix(ctype, "image/") {
			err = fmt.Errorf("content type %q", ctype)
		}
		if err == nil {
			data = "data:" + ctype + ";base64," + base64.StdEncoding.EncodeToString(body)
		} else {
			fmt.Fprintf(os.Stderr, "ink-fetch: image %s skipped: %v\n", u, err)
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
