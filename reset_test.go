package main

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

// withFakeDevbususb points devbususb at a temp dir for the test.
func withFakeDevbususb(t *testing.T) {
	t.Helper()
	old := devbususb
	devbususb = t.TempDir()
	t.Cleanup(func() { devbususb = old })
}

func TestResetDevice(t *testing.T) {
	withFixtureSysfs(t)
	withFakeDevbususb(t)
	// no devnode yet: open must fail
	if err := resetDevice("1-1.4"); err == nil {
		t.Error("resetDevice without a devnode should fail")
	}
	// with the devnode (busnum 1, devnum 4) the ioctl reaches the fake
	// regular file, which the kernel answers with ENOTTY: proof that the
	// busid resolved to the right node and the ioctl was issued
	mkFile(t, devbususb+"/001/004", "")
	if err := resetDevice("1-1.4"); !errors.Is(err, unix.ENOTTY) {
		t.Errorf("resetDevice = %v, want ENOTTY", err)
	}
}

func TestResetMatching(t *testing.T) {
	withFixtureSysfs(t)
	withFakeDevbususb(t)
	if err := resetMatching(mustPattern("nosuchdevice")); err == nil {
		t.Error("resetMatching with no matches should fail")
	}
	mkFile(t, devbususb+"/001/004", "")
	// the fixture's only non-hub device is 1-1.4 (Telink)
	if err := resetMatching(mustPattern("Telink")); !errors.Is(err, unix.ENOTTY) {
		t.Errorf("resetMatching = %v, want ENOTTY", err)
	}
}
