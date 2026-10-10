package stage

import (
	"errors"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"example.org/2026/10/My Post!": "example-org-2026-10-my-post",
		"  Été à Paris  ":              "ete-a-paris",
		"???":                          "item",
		strings.Repeat("a", 100):       strings.Repeat("a", 80),
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRun(t *testing.T) {
	var out strings.Builder
	code := Run("test", strings.NewReader("a\n\n bad \nc\n"), &out, func(line string) ([]string, error) {
		if line == "bad" {
			return nil, errors.New("boom")
		}
		return []string{line + ".out"}, nil
	})
	if code != 1 || out.String() != "a.out\nc.out\n" {
		t.Fatalf("code %d, out %q", code, out.String())
	}
}
