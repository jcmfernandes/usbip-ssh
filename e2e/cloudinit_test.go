//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

const testKey = "ssh-ed25519 AAAATESTKEY e2e"

func get(t *testing.T, port int, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func TestSeedServer(t *testing.T) {
	port, shutdown, err := seedServer(testKey, "QUJD", "52:54:00:00:09:01", "10.0.9.1")
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown()

	code, body := get(t, port, "/user-data")
	if code != http.StatusOK {
		t.Fatalf("user-data: status %d", code)
	}
	if !strings.HasPrefix(body, "#cloud-config\n") {
		t.Errorf("user-data must start with #cloud-config, got: %.40q", body)
	}
	for _, want := range []string{testKey, "disable_root: false", "StrictHostKeyChecking accept-new", "encoding: b64", "10.0.9.1/24"} {
		if !strings.Contains(body, want) {
			t.Errorf("user-data missing %q", want)
		}
	}

	code, body = get(t, port, "/meta-data")
	if code != http.StatusOK || !strings.Contains(body, "instance-id:") {
		t.Errorf("meta-data: status %d body %q", code, body)
	}

	code, _ = get(t, port, "/vendor-data")
	if code != http.StatusNotFound {
		t.Errorf("vendor-data: want 404, got %d", code)
	}
}
