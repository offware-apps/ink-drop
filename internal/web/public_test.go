package web

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPublicOnly(t *testing.T) {
	for addr, ok := range map[string]bool{
		"93.184.215.14:443": true, "[2606:4700::1111]:443": true,
		"127.0.0.1:80": false, "[::1]:80": false, "0.0.0.0:80": false,
		"192.168.1.1:80": false, "10.0.0.5:80": false, "172.16.0.1:80": false, "172.31.255.255:80": false,
		"169.254.169.254:80": false, "[fe80::1]:80": false, "[fd00::1]:80": false,
		"100.64.0.1:80": false, "224.0.0.1:80": false, "[::ffff:192.168.1.1]:80": false,
	} {
		if err := public(addr, nil); (err == nil) != ok {
			t.Errorf("%s: allowed %v, want %v (%v)", addr, err == nil, ok, err)
		}
	}
}

// A page on this machine, the case a share naming a home server makes, is
// never fetched once the dialer holds the check.
func TestPublicOnlyRefusesLocalServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer srv.Close()
	d := &net.Dialer{Timeout: 5 * time.Second, Control: func(_, address string, _ syscall.RawConn) error { return public(address, nil) }}
	client := &http.Client{Transport: &http.Transport{DialContext: d.DialContext}}
	if _, _, _, err := Get(client, srv.URL, 1<<20); err == nil {
		t.Fatal("a server on 127.0.0.1 was fetched")
	}
}

func TestPublicRefusesLocalPrefixesAndNAT64(t *testing.T) {
	local := []netip.Prefix{netip.MustParsePrefix("2001:db8:1f2:3450::/64"), netip.MustParsePrefix("203.0.113.0/24")}
	for addr, ok := range map[string]bool{
		"[2001:db8:1f2:3450::1]:80": false, "[2001:db8:1f2:3451::1]:80": true,
		"203.0.113.9:80": false, "198.51.100.9:80": true,
		"[64:ff9b::c0a8:101]:80": false, "[64:ff9b:1::c0a8:101]:80": false, "[64:ff9b::5db8:d70e]:80": true,
	} {
		if err := public(addr, local); (err == nil) != ok {
			t.Errorf("%s: allowed %v, want %v (%v)", addr, err == nil, ok, err)
		}
	}
}

// INK_PUBLIC_ONLY puts the check on the transport every stage's client uses.
func TestPublicFromEnv(t *testing.T) {
	tr := http.DefaultTransport.(*http.Transport)
	was := tr.DialContext
	t.Cleanup(func() { tr.DialContext = was })
	t.Setenv("INK_PUBLIC_ONLY", "1")
	publicFromEnv()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer srv.Close()
	if _, _, _, err := Get(&http.Client{Timeout: 5 * time.Second}, srv.URL, 1<<20); err == nil {
		t.Fatal("with INK_PUBLIC_ONLY set, a server on 127.0.0.1 was fetched")
	}
}

// A stage started with INK_PUBLIC_ONLY refuses a local server from its first
// request: the test binary runs again as a child with the variable set, so
// the package's own start-up is what applies it.
func TestStartupAppliesPublicOnly(t *testing.T) {
	if u := os.Getenv("INK_TEST_FETCH"); u != "" {
		if _, _, _, err := Get(&http.Client{Timeout: 5 * time.Second}, u, 1<<20); err == nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer srv.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStartupAppliesPublicOnly$")
	cmd.Env = append(os.Environ(), "INK_PUBLIC_ONLY=1", "INK_TEST_FETCH="+srv.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the child fetched the local server, or failed: %v\n%s", err, out)
	}
}

func TestPassing(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{StatusError{503, "503 Service Unavailable"}, true},
		{StatusError{429, "429 Too Many Requests"}, true},
		{StatusError{404, "404 Not Found"}, false},
		{StatusError{403, "403 Forbidden"}, false},
		{&url.Error{Op: "Get", URL: "x", Err: errors.New("timeout")}, true},
		{errors.New("no article found"), false},
	} {
		if got := Passing(c.err); got != c.want {
			t.Errorf("%v: passing %v, want %v", c.err, got, c.want)
		}
	}
}

// A page's images inline up to maxImages in all; the rest keep their address.
func TestImagesBudget(t *testing.T) {
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 5<<20)...)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		w.Write(big)
	}))
	defer srv.Close()
	var page strings.Builder
	page.WriteString("<html><body>")
	for i := range 10 {
		fmt.Fprintf(&page, `<img src="%s/%d.png">`, srv.URL, i)
	}
	page.WriteString("</body></html>")
	base, _ := url.Parse(srv.URL)
	out, err := Inline(srv.Client(), []byte(page.String()), "text/html", base)
	if err != nil {
		t.Fatal(err)
	}
	inlined := strings.Count(string(out), "data:image/png")
	if inlined != 6 || len(out) > 4*maxImages/3+1<<20 {
		t.Errorf("want 6 of 10 images inlined under a 32 MB budget, got %d in %d bytes", inlined, len(out))
	}
	// Once the budget is spent, no image is even asked for: six fit, a
	// seventh finds 2 MB left, and the last three are never fetched.
	if hits != 7 {
		t.Errorf("want 7 requests, got %d", hits)
	}
}

// A body that is no image, read in full, spends the budget too.
func TestImagesBudgetCountsRefusedBodies(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/html")
		w.Write(make([]byte, 5<<20))
	}))
	defer srv.Close()
	var page strings.Builder
	page.WriteString("<html><body>")
	for i := range 10 {
		fmt.Fprintf(&page, `<img src="%s/%d.png">`, srv.URL, i)
	}
	page.WriteString("</body></html>")
	base, _ := url.Parse(srv.URL)
	if _, err := Inline(srv.Client(), []byte(page.String()), "text/html", base); err != nil {
		t.Fatal(err)
	}
	if hits != 7 {
		t.Errorf("want 7 requests before the budget ends, got %d", hits)
	}
}

// An image over the per-image limit, refused while budget is left, spends the
// bytes read for it: ten of 11 MB stop after four requests.
func TestImagesBudgetCountsOversizedImages(t *testing.T) {
	hits := 0
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 11<<20)...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		w.Write(big)
	}))
	defer srv.Close()
	var page strings.Builder
	page.WriteString("<html><body>")
	for i := range 10 {
		fmt.Fprintf(&page, `<img src="%s/%d.png">`, srv.URL, i)
	}
	page.WriteString("</body></html>")
	base, _ := url.Parse(srv.URL)
	if _, err := Inline(srv.Client(), []byte(page.String()), "text/html", base); err != nil {
		t.Fatal(err)
	}
	if hits != 4 {
		t.Errorf("want 4 requests, got %d", hits)
	}
}
