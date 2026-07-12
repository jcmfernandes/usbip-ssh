//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
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

const kbdID = "0627:0001" // QEMU USB Keyboard

// lsDevs prints one line per USB device in the guest:
// "<busid> <vid:pid> <driver> <resolved sysfs path>"
const lsDevs = `for d in /sys/bus/usb/devices/[0-9]*; do
b=${d##*/}
case $b in *:*) continue;; esac
[ -f "$d/idVendor" ] || continue
drv=none
[ -L "$d/driver" ] && drv=$(basename $(readlink "$d/driver"))
echo "$b $(cat $d/idVendor):$(cat $d/idProduct) $drv $(readlink -f $d)"
done`

type guestDev struct {
	busid, vidpid, driver, path string
}

func (v *vm) usbDevs() ([]guestDev, error) {
	out, err := v.ssh(lsDevs)
	if err != nil {
		return nil, fmt.Errorf("listing guest usb devices: %v: %s", err, out)
	}
	var devs []guestDev
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if f := strings.Fields(line); len(f) == 4 {
			devs = append(devs, guestDev{f[0], f[1], f[2], f[3]})
		}
	}
	return devs, nil
}

// kbds splits the emulated keyboards into the vhci-attached copies and
// the normal devices (idle, or exported and bound to usbip-host).
func kbds(devs []guestDev) (vhci, normal []guestDev) {
	for _, d := range devs {
		if d.vidpid != kbdID {
			continue
		}
		if strings.Contains(d.path, "vhci_hcd") {
			vhci = append(vhci, d)
		} else {
			normal = append(normal, d)
		}
	}
	return
}

func mustWait(t *testing.T, desc string, timeout time.Duration, cond func() (bool, error)) {
	t.Helper()
	if err := waitFor(desc, timeout, cond); err != nil {
		t.Fatal(err)
	}
}

// resetState returns the guest to a clean slate: nothing attached, no
// emulated keyboards plugged in.
func resetState(t *testing.T, v *vm) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := v.ssh("echo '--- usb devices:'; " + lsDevs + "; echo '--- dmesg tail:'; dmesg | tail -30; echo '--- logs:'; tail -n +1 /tmp/*.log 2>/dev/null")
			t.Logf("guest state after failure:\n%s", out)
		}
	})
	v.ssh("usbip-ssh detach all") // error ignored: nothing may be attached
	v.qmp.deviceDel("kbd0")       // error ignored: may not be plugged
	v.qmp.deviceDel("kbd1")
	mustWait(t, "guest to quiesce", 30*time.Second, func() (bool, error) {
		devs, err := v.usbDevs()
		if err != nil {
			return false, err
		}
		vh, norm := kbds(devs)
		return len(vh) == 0 && len(norm) == 0, nil
	})
}

// plugKbd hot-plugs an emulated keyboard and waits for the guest to see it.
func plugKbd(t *testing.T, v *vm, id string, wantNormal int) {
	t.Helper()
	if err := v.qmp.deviceAdd("usb-kbd", id); err != nil {
		t.Fatalf("device_add %s: %v", id, err)
	}
	mustWait(t, fmt.Sprintf("keyboard %s visible in the guest", id), 30*time.Second, func() (bool, error) {
		devs, err := v.usbDevs()
		if err != nil {
			return false, err
		}
		_, norm := kbds(devs)
		return len(norm) == wantNormal, nil
	})
}

func TestList(t *testing.T) {
	v := sharedVM(t)
	resetState(t, v)
	plugKbd(t, v, "kbd0", 1)

	out, err := v.ssh("usbip-ssh list root@localhost")
	if err != nil {
		t.Fatalf("list root@localhost: %v: %s", err, out)
	}
	if !strings.Contains(out, kbdID) {
		t.Errorf("remote list missing %s:\n%s", kbdID, out)
	}

	out, err = v.ssh("usbip-ssh list --local")
	if err != nil {
		t.Fatalf("list --local: %v: %s", err, out)
	}
	if !strings.Contains(out, kbdID) {
		t.Errorf("local list missing %s:\n%s", kbdID, out)
	}
}
