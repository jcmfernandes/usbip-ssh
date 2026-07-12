//go:build e2e

package e2e

import (
	"fmt"
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
