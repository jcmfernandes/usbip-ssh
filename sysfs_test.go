package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// writeTree materializes a map of relative-path → content under a temp dir.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	base := t.TempDir()
	for path, content := range files {
		full := filepath.Join(base, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return base
}

func TestReadFile(t *testing.T) {
	base := writeTree(t, map[string]string{"f": "hello \n"})
	if got := readFile(base + "/f"); got != "hello" {
		t.Errorf("readFile = %q, want %q", got, "hello")
	}
	if got := readFile(base + "/missing"); got != "" {
		t.Errorf("readFile(missing) = %q, want empty", got)
	}
}

func TestReadUevent(t *testing.T) {
	base := writeTree(t, map[string]string{"uevent": "DRIVER=usb\nBUSNUM=001\n"})
	m := readUevent(base + "/uevent")
	if m["DRIVER"] != "usb" || m["BUSNUM"] != "001" {
		t.Errorf("readUevent = %v", m)
	}
	if len(readUevent(base+"/missing")) != 0 {
		t.Error("readUevent(missing) should be empty")
	}
}

func TestXreadFile(t *testing.T) {
	base := writeTree(t, map[string]string{"ok": "42\n", "empty": ""})
	if got, err := xreadFile(base + "/ok"); err != nil || got != "42" {
		t.Errorf("xreadFile = %q, %v", got, err)
	}
	if _, err := xreadFile(base + "/empty"); err == nil {
		t.Error("xreadFile(empty) should fail")
	}
	if _, err := xreadFile(base + "/missing"); err == nil {
		t.Error("xreadFile(missing) should fail")
	}
}

func TestXwriteFile(t *testing.T) {
	base := t.TempDir()
	if err := xwriteFile(base+"/f", "data"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(base + "/f")
	if string(b) != "data" {
		t.Errorf("wrote %q", b)
	}
	if err := xwriteFile(base+"/no/such/dir/f", "x"); err == nil {
		t.Error("xwriteFile to missing dir should fail")
	}
}

// The unbind call site distinguishes a race-loser ENODEV from a genuine
// failure via errors.Is, so xwriteFile's wrapping must keep the errno
// unwrappable rather than flattening it into an opaque string.
func TestXwriteFileErrno(t *testing.T) {
	dir := t.TempDir()
	err := xwriteFile(dir, "x") // writing to a directory: EISDIR
	if !errors.Is(err, unix.EISDIR) {
		t.Errorf("xwriteFile(dir) = %v, want an error wrapping EISDIR", err)
	}
}

func TestFindFiles(t *testing.T) {
	base := writeTree(t, map[string]string{
		"a/dev":     "8:0",
		"a/b/dev":   "8:1",
		"a/b/other": "x",
	})
	// symlinked dirs must not be followed
	if err := os.Symlink(base+"/a", base+"/loop"); err != nil {
		t.Fatal(err)
	}
	var got []string
	findFiles(base, "dev", func(p string) { got = append(got, readFile(p)) })
	if len(got) != 2 {
		t.Errorf("findFiles found %v, want 2 files", got)
	}
}
