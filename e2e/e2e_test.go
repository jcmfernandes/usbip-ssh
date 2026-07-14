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

const (
	devIP = "10.0.9.1"
	impIP = "10.0.9.2"
)

type pair struct {
	dev *vm
	imp *vm
}

var (
	runDir   string
	pairOnce sync.Once
	pairInst *pair
	pairErr  error
)

// sharedPair lazily boots the dev/imp VM pair shared by all scenario tests.
func sharedPair(t *testing.T) *pair {
	t.Helper()
	pairOnce.Do(func() {
		pairInst, pairErr = bootPair(runDir)
	})
	if pairErr != nil {
		t.Fatalf("VM pair boot: %v", pairErr)
	}
	return pairInst
}

func TestMain(m *testing.M) {
	var err error
	runDir, err = os.MkdirTemp("", "usbip-ssh-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	if pairInst != nil {
		pairInst.dev.teardown()
		pairInst.imp.teardown()
	}
	if code == 0 && pairErr == nil {
		os.RemoveAll(runDir)
	} else {
		fmt.Fprintf(os.Stderr, "e2e: VM artifacts kept in %s (dev/, imp/)\n", runDir)
	}
	os.Exit(code)
}

func TestPairBoot(t *testing.T) {
	p := sharedPair(t)
	for name, v := range map[string]*vm{"dev": p.dev, "imp": p.imp} {
		if out, err := v.ssh("uname -r"); err != nil || strings.TrimSpace(out) == "" {
			t.Fatalf("[%s] uname: %v: %s", name, err, out)
		}
		if out, err := v.ssh("modinfo usbip-host vhci-hcd >/dev/null && echo ok"); err != nil || !strings.Contains(out, "ok") {
			t.Fatalf("[%s] guest kernel lacks usbip modules: %v: %s", name, err, out)
		}
		if out, err := v.ssh("usbip-ssh list --local"); err != nil {
			t.Fatalf("[%s] usbip-ssh binary doesn't run: %v: %s", name, err, out)
		}
	}
	// The whole point of the pair: each VM can ssh the other over the link.
	if out, err := p.dev.ssh("ssh -o BatchMode=yes -o ConnectTimeout=5 root@" + impIP + " true"); err != nil {
		t.Fatalf("dev->imp ssh over link failed: %v: %s", err, out)
	}
	if out, err := p.imp.ssh("ssh -o BatchMode=yes -o ConnectTimeout=5 root@" + devIP + " true"); err != nil {
		t.Fatalf("imp->dev ssh over link failed: %v: %s", err, out)
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

func TestList(t *testing.T) {
	t.Skip("pending two-VM conversion (Task 2)")
}

func TestAttachDetach(t *testing.T) {
	t.Skip("pending two-VM conversion (Task 2)")
}

func TestUnbind(t *testing.T) {
	t.Skip("pending two-VM conversion (Task 2)")
}

func TestVhubHotAttach(t *testing.T) {
	t.Skip("pending two-VM conversion (Task 2)")
}

func TestKeepReconnect(t *testing.T) {
	t.Skip("pending two-VM conversion (Task 2)")
}

func TestReverseAttachDetach(t *testing.T) {
	t.Skip("pending two-VM conversion (Task 3)")
}

func TestReverseUnbind(t *testing.T) {
	t.Skip("pending two-VM conversion (Task 3)")
}

func TestReverseVhubHotAttach(t *testing.T) {
	t.Skip("pending two-VM conversion (Task 3)")
}

func TestReverseKeepReconnect(t *testing.T) {
	t.Skip("pending two-VM conversion (Task 3)")
}
