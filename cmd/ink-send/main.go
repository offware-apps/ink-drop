// Command ink-send is a browser native-messaging host: when the ink-drop
// extension asks, it runs the one command the user wrote in its config file.
// It reads nothing else from the browser, so the extension cannot change what
// runs. `ink-send -install` registers it with Firefox for the current user.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The names the extension and Firefox use for this host.
const (
	hostName    = "ink_drop"
	extensionID = "ink-drop@offware-apps.github.io"
)

type reply struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

func main() {
	install := flag.Bool("install", false, "register with Firefox for this user, then exit")
	flag.Parse()
	conf, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ink-send:", err)
		os.Exit(1)
	}
	commandFile := filepath.Join(conf, "ink-drop", "send")
	if *install {
		path, err := register()
		if err != nil {
			fmt.Fprintln(os.Stderr, "ink-send:", err)
			os.Exit(1)
		}
		fmt.Printf("ink-send: registered in %s\nink-send: the command it runs is read from %s\n", path, commandFile)
		return
	}
	// The message only says "send": its content is read and dropped.
	out, err := "", read(os.Stdin)
	if err == nil {
		out, err = send(commandFile)
	}
	res := reply{OK: err == nil, Output: out}
	if err != nil {
		res.Output = strings.TrimSpace(out + "\n" + err.Error())
	}
	if err := write(os.Stdout, res); err != nil {
		fmt.Fprintln(os.Stderr, "ink-send:", err)
		os.Exit(1)
	}
}

// read consumes one message: a 32-bit length in native byte order, then that
// many bytes, at most 1 KB since the message carries nothing.
func read(r io.Reader) error {
	var n uint32
	if err := binary.Read(r, binary.NativeEndian, &n); err != nil {
		return err
	}
	if n > 1024 {
		return fmt.Errorf("message of %d bytes is over 1 KB", n)
	}
	_, err := io.CopyN(io.Discard, r, int64(n))
	return err
}

// write encodes v as one message: its JSON length, then the JSON.
func write(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := binary.Write(w, binary.NativeEndian, uint32(len(b))); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// send runs the command in commandFile and returns the last 4 KB it printed.
func send(commandFile string) (string, error) {
	b, err := os.ReadFile(commandFile)
	command := strings.TrimSpace(string(b))
	if errors.Is(err, os.ErrNotExist) || (err == nil && command == "") {
		return "", fmt.Errorf("write the send command into %s", commandFile)
	}
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-c", command).CombinedOutput()
	if len(out) > 4096 {
		out = out[len(out)-4096:]
	}
	return strings.TrimSpace(string(out)), err
}

// register writes the manifest Firefox reads to find this host, and returns
// its path.
func register() (string, error) {
	dirs := map[string]string{
		"linux":  ".mozilla/native-messaging-hosts",
		"darwin": "Library/Application Support/Mozilla/NativeMessagingHosts",
	}
	dir, ok := dirs[runtime.GOOS]
	if !ok {
		return "", fmt.Errorf("-install supports Linux and macOS, not %s", runtime.GOOS)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	manifest, err := json.MarshalIndent(map[string]any{
		"name":               hostName,
		"description":        "Sends the ink-drop library on with the user's command",
		"path":               exe,
		"type":               "stdio",
		"allowed_extensions": []string{extensionID},
	}, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, dir, hostName+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(manifest, '\n'), 0o644)
}
