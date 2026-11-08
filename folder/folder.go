// Package folder reads and writes the library folder every ink-drop stage
// shares, per docs/folder.md.
package folder

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Schema is the sidecar version this package writes and accepts.
const Schema = 1

// SidecarDir holds one JSON sidecar per item, hidden so a reader's library
// lists the documents alone.
const SidecarDir = ".ink"

// Meta is the sidecar of one item: where it came from and which stage made it.
type Meta struct {
	Schema  int       `json:"schema"`
	File    string    `json:"file"`
	Title   string    `json:"title,omitempty"`
	Source  string    `json:"source"`
	Fetched time.Time `json:"fetched"`
	Stage   string    `json:"stage"`
	// What a feed says of its entry, where it says it: when it was published,
	// by whom, and the feed's own categories for it.
	Published time.Time `json:"published,omitzero"`
	Author    string    `json:"author,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
}

// Put writes data as name inside dir with its sidecar, and returns the
// document's path. The sidecar lands first, so a document is never seen
// without one.
func Put(dir, name string, data []byte, m Meta) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("folder: invalid item name %q", name)
	}
	m.Schema, m.File = Schema, name
	side, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(dir, SidecarDir), 0o755); err != nil {
		return "", err
	}
	if err := WriteAtomic(filepath.Join(dir, SidecarDir, name+".json"), append(side, '\n')); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	return path, WriteAtomic(path, data)
}

// Free returns the name for an item made from source in dir: stem plus ext,
// numbered while another source's item holds it, so an item never replaces
// another page's. An item of the same source keeps its name and is replaced.
func Free(dir, stem, ext, source string) string {
	name := stem + ext
	for n := 2; held(dir, name, source); n++ {
		name = fmt.Sprintf("%s-%d%s", stem, n, ext)
	}
	return name
}

// held reports whether dir holds an item by that name made from a page other
// than source, or a file by that name with no sidecar.
func held(dir, name, source string) bool {
	m, err := Get(filepath.Join(dir, name))
	if err != nil {
		_, statErr := os.Stat(filepath.Join(dir, name))
		return statErr == nil
	}
	return m.Source != source
}

// Remove deletes the item at path and its sidecar.
func Remove(path string) error {
	os.Remove(filepath.Join(filepath.Dir(path), SidecarDir, filepath.Base(path)+".json"))
	return os.Remove(path)
}

// Get returns the sidecar of the document at path.
func Get(path string) (Meta, error) {
	var m Meta
	b, err := os.ReadFile(filepath.Join(filepath.Dir(path), SidecarDir, filepath.Base(path)+".json"))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	if m.Schema != Schema {
		return m, fmt.Errorf("folder: sidecar schema %d, want %d", m.Schema, Schema)
	}
	if m.File != filepath.Base(path) {
		return m, errors.New("folder: sidecar names another file")
	}
	return m, nil
}

// WriteAtomic renames a finished temporary file into place, so a sync tool
// never copies half a document.
func WriteAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".ink-tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	// CreateTemp makes the file private; a library is read by sync tools too.
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
