package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func message(body string) *bytes.Buffer {
	var b bytes.Buffer
	binary.Write(&b, binary.NativeEndian, uint32(len(body)))
	b.WriteString(body)
	return &b
}

func TestRead(t *testing.T) {
	if err := read(message("{}")); err != nil {
		t.Errorf("the extension's message was refused: %v", err)
	}
	// One byte over the cap, and otherwise a whole message.
	if err := read(message(strings.Repeat(" ", 1025))); err == nil {
		t.Error("a message over 1 KB was accepted")
	}
	short := message("{}")
	short.Truncate(5)
	if err := read(short); err == nil {
		t.Error("a message shorter than its length was accepted")
	}
}

func TestWrite(t *testing.T) {
	var b bytes.Buffer
	if err := write(&b, reply{OK: true, Output: "sent"}); err != nil {
		t.Fatal(err)
	}
	var n uint32
	binary.Read(&b, binary.NativeEndian, &n)
	if int(n) != b.Len() || b.String() != `{"ok":true,"output":"sent"}` {
		t.Errorf("length %d, body %q", n, b.String())
	}
}

func TestSend(t *testing.T) {
	commandFile := filepath.Join(t.TempDir(), "send")
	if _, err := send(commandFile); err == nil || !strings.Contains(err.Error(), commandFile) {
		t.Errorf("a missing command file gave %v", err)
	}
	if err := os.WriteFile(commandFile, []byte("echo sent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := send(commandFile); err != nil || out != "sent" {
		t.Errorf("got %q, %v", out, err)
	}
}

func TestRegister(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := register()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Name, Path, Type  string
		AllowedExtensions []string `json:"allowed_extensions"`
	}
	if err := json.Unmarshal(b, &m); err != nil || m.Name != hostName || m.Type != "stdio" || !filepath.IsAbs(m.Path) ||
		len(m.AllowedExtensions) != 1 || m.AllowedExtensions[0] != extensionID || filepath.Base(path) != hostName+".json" {
		t.Errorf("manifest %s: %+v, %v", path, m, err)
	}
}
