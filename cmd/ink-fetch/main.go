// Command ink-fetch downloads each URL read from stdin into the work folder: a
// PDF as it is, an HTML page with its images inlined so later stages stay
// offline.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/offware-apps/ink-drop/folder"
	"github.com/offware-apps/ink-drop/internal/stage"
	"github.com/offware-apps/ink-drop/internal/web"
)

const maxPage = 64 << 20

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
	body, ctype, final, err := web.Get(client, u.String(), maxPage)
	if err != nil {
		return "", err
	}
	stem := stage.Slug(final.Host + strings.TrimSuffix(final.Path, path.Ext(final.Path)))
	meta := folder.Meta{Source: final.String(), Fetched: time.Now().UTC().Truncate(time.Second), Stage: "ink-fetch"}
	switch {
	case ctype == "application/pdf" || bytes.HasPrefix(body, []byte("%PDF-")):
		return folder.Put(dir, stem+".pdf", body, meta)
	case ctype == "text/html" || ctype == "application/xhtml+xml":
		page, err := web.Inline(client, body, ctype, final)
		if err != nil {
			return "", err
		}
		return folder.Put(dir, stem+".html", page, meta)
	}
	return "", fmt.Errorf("unsupported content type %q", ctype)
}
