//go:build e2e

package e2e

import (
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// waitFor polls cond every 500ms until it returns true or timeout
// expires. On timeout the error names desc and the last error (or
// "condition false") from cond.
func waitFor(desc string, timeout time.Duration, cond func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	lastErr := fmt.Errorf("condition false")
	for {
		ok, err := cond()
		if ok {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %v waiting for %s: %v", timeout, desc, lastErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

const (
	imageName = "debian-13-generic-amd64.qcow2"
	imageBase = "https://cloud.debian.org/images/cloud/trixie/latest/"
)

// expectedSum extracts the hash for name from SHA512SUMS-format content.
func expectedSum(sums, name string) (string, error) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no entry for %s in SHA512SUMS", name)
}

func fileSHA512(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func download(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return fmt.Errorf("GET %s: %w", url, err)
	}
	return f.Close()
}

// baseImage returns the path of the verified cached debian image,
// downloading it and its SHA512SUMS on first use. Cached runs need no
// network: the image is re-verified against the cached sums file.
func baseImage() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "usbip-ssh-e2e")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	img := filepath.Join(dir, imageName)
	sums := filepath.Join(dir, "SHA512SUMS")
	if _, err := os.Stat(img); err != nil {
		log.Printf("e2e: downloading %s (~400 MB, one-time, cached in %s)", imageBase+imageName, dir)
		if err := download(imageBase+"SHA512SUMS", sums); err != nil {
			return "", err
		}
		if err := download(imageBase+imageName, img+".part"); err != nil {
			return "", err
		}
		if err := os.Rename(img+".part", img); err != nil {
			return "", err
		}
	}
	sumsContent, err := os.ReadFile(sums)
	if err != nil {
		return "", fmt.Errorf("%v (delete %s to re-download)", err, img)
	}
	want, err := expectedSum(string(sumsContent), imageName)
	if err != nil {
		return "", err
	}
	got, err := fileSHA512(img)
	if err != nil {
		return "", err
	}
	if got != want {
		return "", fmt.Errorf("%s: checksum mismatch (delete it to re-download)", img)
	}
	return img, nil
}
