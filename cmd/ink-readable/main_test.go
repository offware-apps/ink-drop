package main

import (
	"os"
	"strings"
	"testing"

	"github.com/offware-apps/ink-drop/folder"
)

const page = `<html lang="fr"><head><title>Un article | Le site</title></head><body>
<nav><a href="/">Accueil</a><a href="/blog">Blog</a></nav>
<article><h1>Un article</h1>
<p>Premier paragraphe, assez long pour que l'extraction le garde comme contenu principal de la page, avec quelques phrases de plus.</p>
<p>Second paragraphe, lui aussi assez long pour compter, qui parle d'autre chose et ajoute du texte au corps de l'article.</p>
<img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==" alt="pixel">
</article>
<footer>Mentions légales</footer></body></html>`

func TestReadable(t *testing.T) {
	in := t.TempDir()
	p, err := folder.Put(in, "post.html", []byte(page), folder.Meta{Source: "https://example.org/post", Stage: "ink-fetch"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := readable(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	got := string(b)
	if !strings.Contains(got, "Premier paragraphe") || !strings.Contains(got, "data:image/png;base64,") || !strings.Contains(got, `lang="fr"`) {
		t.Errorf("article text, image or language lost: %s", got)
	}
	if strings.Contains(got, "Accueil") || strings.Contains(got, "Mentions") {
		t.Errorf("navigation or footer kept: %s", got)
	}
	m, err := folder.Get(out)
	if err != nil || m.Title == "" || m.Source != "https://example.org/post" || m.Stage != "ink-readable" {
		t.Errorf("sidecar %+v, %v", m, err)
	}
}

func TestReadablePassesPDF(t *testing.T) {
	p, err := folder.Put(t.TempDir(), "paper.pdf", []byte("%PDF-"), folder.Meta{Source: "https://example.org/paper.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := readable(t.TempDir(), p)
	if err != nil || out != p {
		t.Fatalf("got %s, %v", out, err)
	}
}
