//go:build e2e

package e2e

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWaitForSucceeds(t *testing.T) {
	calls := 0
	err := waitFor("thing", 5*time.Second, func() (bool, error) {
		calls++
		return calls >= 2, nil
	})
	if err != nil {
		t.Fatalf("waitFor: %v", err)
	}
	if calls < 2 {
		t.Fatalf("cond called %d times, want >= 2", calls)
	}
}

func TestWaitForTimesOut(t *testing.T) {
	err := waitFor("frob the knob", 1*time.Millisecond, func() (bool, error) {
		return false, errors.New("still frobbing")
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "frob the knob") || !strings.Contains(err.Error(), "still frobbing") {
		t.Fatalf("error should name the wait and the last error, got: %v", err)
	}
}

func TestFileSHA512(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	// the well-known SHA-512 test vector for "abc"
	want := "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a" +
		"2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f"
	got, err := fileSHA512(p)
	if err != nil || got != want {
		t.Fatalf("got %q, %v", got, err)
	}
}
