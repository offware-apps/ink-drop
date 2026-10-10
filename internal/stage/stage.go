// Package stage holds what every ink-drop command shares: the stdin loop and
// the output folder, per docs/folder.md.
package stage

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Run calls fn on each non-blank line of in and prints the paths it returns,
// one per line. It returns the exit code: 1 if any line failed.
func Run(name string, in io.Reader, out io.Writer, fn func(line string) ([]string, error)) int {
	code := 0
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		paths, err := fn(line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s: %v\n", name, line, err)
			code = 1
			continue
		}
		for _, p := range paths {
			fmt.Fprintln(out, p)
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: reading stdin: %v\n", name, err)
		code = 1
	}
	return code
}

// Dir returns the output folder: to when set, else ink-drop/<name> under the
// user's cache folder.
func Dir(to, name string) (string, error) {
	if to != "" {
		return to, nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "ink-drop", name), nil
}

// Slug returns s as lowercase ASCII letters and digits joined by hyphens, accents
// dropped, at most 80 bytes, or "item" when nothing is left.
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
			if b.Len() >= 80 {
				break
			}
			continue
		}
		dash = true
	}
	if b.Len() == 0 {
		return "item"
	}
	return b.String()
}
