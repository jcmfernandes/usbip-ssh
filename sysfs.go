package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// readFile returns the file's contents with trailing whitespace removed,
// or "" if it cannot be read.
func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), " \t\n")
}

// readUevent parses a sysfs uevent file of KEY=VALUE lines.
func readUevent(path string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(readFile(path), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			m[k] = v
		}
	}
	return m
}

func xreadFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	d := strings.TrimRight(string(b), " \t\n")
	if d == "" {
		return "", fmt.Errorf("empty file %s", path)
	}
	deb("READ %s > %s", path, d)
	return d, nil
}

func xwriteFile(path, data string) error {
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		return fmt.Errorf("write %s < %s: %w", path, data, err)
	}
	deb("WRITE %s < %s", path, data)
	return nil
}

func xsystem(argv ...string) error {
	deb("EXEC %s", strings.Join(argv, " "))
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("system %s: %w", strings.Join(argv, " "), err)
	}
	return nil
}

// findFiles recursively walks dir (not following symlinks) and calls cb for
// every entry whose name equals name.
func findFiles(dir, name string, cb func(path string)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := dir + "/" + e.Name()
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		if e.IsDir() {
			findFiles(p, name, cb)
		}
		if e.Name() == name {
			cb(p)
		}
	}
}
