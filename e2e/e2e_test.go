//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

var (
	runDir string
	vmOnce sync.Once
	vmInst *vm
	vmErr  error
)

// sharedVM lazily boots the single VM shared by all scenario tests.
// Unit tests that don't call it never require qemu.
func sharedVM(t *testing.T) *vm {
	t.Helper()
	vmOnce.Do(func() {
		vmInst, vmErr = bootVM(runDir)
	})
	if vmErr != nil {
		t.Fatalf("VM boot: %v", vmErr)
	}
	return vmInst
}

func TestMain(m *testing.M) {
	var err error
	runDir, err = os.MkdirTemp("", "usbip-ssh-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	if vmInst != nil {
		vmInst.teardown()
	}
	if code == 0 && vmErr == nil {
		os.RemoveAll(runDir)
	} else {
		fmt.Fprintf(os.Stderr, "e2e: VM artifacts kept in %s (console.log, qemu.log)\n", runDir)
	}
	os.Exit(code)
}

func TestVMBoot(t *testing.T) {
	v := sharedVM(t)
	out, err := v.ssh("uname -r")
	if err != nil || strings.TrimSpace(out) == "" {
		t.Fatalf("uname: %v: %s", err, out)
	}
	if out, err := v.ssh("modinfo usbip-host vhci-hcd >/dev/null && echo ok"); err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("guest kernel lacks usbip modules: %v: %s", err, out)
	}
	if out, err := v.ssh("usbip-ssh list --local"); err != nil {
		t.Fatalf("usbip-ssh binary doesn't run in the guest: %v: %s", err, out)
	}
}
