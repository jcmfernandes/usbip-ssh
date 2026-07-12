//go:build e2e

package e2e

import (
	"errors"
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
