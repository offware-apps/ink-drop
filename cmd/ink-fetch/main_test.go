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

func server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/post", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=iso-8859-1")
		w.Write([]byte("<html><body><p>caf\xe9</p>" +
			`<img class="lazy" src="data:image/gif;base64,R0lGOD" data-src="/a.png" srcset="/big.png 2x">` +
			`<picture><source srcset="/big.png"><img src="/a.png"></picture>` +
			`<img src="/missing.png"></body></html>`))
	})
	mux.HandleFunc("/a.png", func(w http.ResponseWriter, r *http.Request) { w.Write(png) })
	mux.HandleFunc("/paper.pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("%PDF-1.7 body"))
	})
	return httptest.NewServer(mux)
}

func TestFetchInlinesImages(t *testing.T) {
	srv := server()
	defer srv.Close()
	dir := t.TempDir()
	p, err := fetch(srv.Client(), dir, srv.URL+"/post")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	page := string(b)
	if strings.Count(page, "data:image/png;base64,") != 2 {
		t.Errorf("want both copies of a.png inlined, got %s", page)
	}
	for _, gone := range []string{"srcset", "data-src", "lazy", "big.png", "<source"} {
		if strings.Contains(page, gone) {
			t.Errorf("page still holds %q", gone)
		}
	}
	if !strings.Contains(page, "café") || !strings.Contains(page, srv.URL+"/missing.png") {
		t.Errorf("want UTF-8 text and the failed image's address kept, got %s", page)
	}
	m, err := folder.Get(p)
	if err != nil || m.Source != srv.URL+"/post" || m.Stage != "ink-fetch" {
		t.Errorf("sidecar %+v, %v", m, err)
	}
}

func TestFetchPDF(t *testing.T) {
	srv := server()
	defer srv.Close()
	p, err := fetch(srv.Client(), t.TempDir(), srv.URL+"/paper.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(p) != ".pdf" || !strings.HasSuffix(strings.TrimSuffix(p, ".pdf"), "paper") {
		t.Errorf("got %s", p)
	}
}

func TestFetchRejectsScheme(t *testing.T) {
	if _, err := fetch(http.DefaultClient, t.TempDir(), "file:///etc/passwd"); err == nil {
		t.Fatal("fetch accepted a file URL")
	}
}
