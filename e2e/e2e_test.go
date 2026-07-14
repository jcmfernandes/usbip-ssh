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

// dumpState logs each guest's usb + dmesg + session logs after a failure.
func dumpState(t *testing.T, p *pair) {
	t.Helper()
	for _, e := range []struct {
		name string
		v    *vm
	}{{"dev", p.dev}, {"imp", p.imp}} {
		out, _ := e.v.ssh("echo '--- usb devices:'; " + lsDevs + "; echo '--- dmesg tail:'; dmesg | tail -30; echo '--- logs:'; tail -n +1 /tmp/*.log 2>/dev/null")
		t.Logf("[%s] guest state after failure:\n%s", e.name, out)
	}
}

// resetState returns both guests to a clean slate: nothing attached on imp,
// nothing exported on dev, and no emulated keyboards plugged into dev.
func resetState(t *testing.T, p *pair) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			dumpState(t, p)
		}
	})
	p.imp.ssh("usbip-ssh detach all")        // importer: drop any vhci ports
	p.dev.ssh("usbip-ssh unbind -r " + kbdID) // exporter: rebind any exported kbd
	p.dev.qmp.deviceDel("kbd0")               // errors ignored: may not be plugged
	p.dev.qmp.deviceDel("kbd1")
	mustWait(t, "guests to quiesce", 30*time.Second, noKbds(p))
}

// plugKbd hot-plugs an emulated keyboard into dev and waits for dev to see
// wantDev keyboards total.
func plugKbd(t *testing.T, p *pair, id string, wantDev int) {
	t.Helper()
	if err := p.dev.qmp.deviceAdd("usb-kbd", id); err != nil {
		t.Fatalf("device_add %s: %v", id, err)
	}
	mustWait(t, fmt.Sprintf("keyboard %s visible on dev", id), 30*time.Second, func() (bool, error) {
		devs, err := p.dev.usbDevs()
		if err != nil {
			return false, err
		}
		_, norm := kbds(devs)
		return len(norm) == wantDev, nil
	})
}

// attachedKbds reports that imp has want vhci copies and dev has want
// originals exported (bound to usbip-host).
func attachedKbds(p *pair, want int) func() (bool, error) {
	return func() (bool, error) {
		idevs, err := p.imp.usbDevs()
		if err != nil {
			return false, err
		}
		ivh, _ := kbds(idevs)
		if len(ivh) != want {
			return false, fmt.Errorf("imp has %d vhci keyboards, want %d", len(ivh), want)
		}
		ddevs, err := p.dev.usbDevs()
		if err != nil {
			return false, err
		}
		_, dnorm := kbds(ddevs)
		if len(dnorm) != want {
			return false, fmt.Errorf("dev has %d keyboards, want %d", len(dnorm), want)
		}
		for _, d := range dnorm {
			if d.driver != "usbip-host" {
				return false, fmt.Errorf("dev keyboard %s bound to %q, want usbip-host", d.busid, d.driver)
			}
		}
		return true, nil
	}
}

// releasedKbds reports that imp has no vhci copy and dev has count keyboards
// back on the normal usb driver.
func releasedKbds(p *pair, count int) func() (bool, error) {
	return func() (bool, error) {
		idevs, err := p.imp.usbDevs()
		if err != nil {
			return false, err
		}
		ivh, _ := kbds(idevs)
		if len(ivh) != 0 {
			return false, fmt.Errorf("imp still has %d vhci keyboards, want 0", len(ivh))
		}
		ddevs, err := p.dev.usbDevs()
		if err != nil {
			return false, err
		}
		_, dnorm := kbds(ddevs)
		if len(dnorm) != count {
			return false, fmt.Errorf("dev has %d keyboards, want %d", len(dnorm), count)
		}
		for _, d := range dnorm {
			if d.driver != "usb" {
				return false, fmt.Errorf("dev keyboard %s bound to %q, want usb", d.busid, d.driver)
			}
		}
		return true, nil
	}
}

// noKbds reports that no keyboard is present on either guest.
func noKbds(p *pair) func() (bool, error) {
	return func() (bool, error) {
		idevs, err := p.imp.usbDevs()
		if err != nil {
			return false, err
		}
		if ivh, _ := kbds(idevs); len(ivh) != 0 {
			return false, fmt.Errorf("imp still has %d vhci keyboards", len(ivh))
		}
		ddevs, err := p.dev.usbDevs()
		if err != nil {
			return false, err
		}
		dvh, dnorm := kbds(ddevs)
		if len(dvh) != 0 || len(dnorm) != 0 {
			return false, fmt.Errorf("dev still has %d vhci / %d normal keyboards", len(dvh), len(dnorm))
		}
		return true, nil
	}
}

func TestList(t *testing.T) {
	p := sharedPair(t)
	resetState(t, p)
	plugKbd(t, p, "kbd0", 1)

	// remote list: imp lists dev's exportable devices
	out, err := p.imp.ssh("usbip-ssh list root@" + devIP)
	if err != nil {
		t.Fatalf("list root@dev: %v: %s", err, out)
	}
	if !strings.Contains(out, kbdID) {
		t.Errorf("remote list missing %s:\n%s", kbdID, out)
	}

	// local list on the device owner
	out, err = p.dev.ssh("usbip-ssh list --local")
	if err != nil {
		t.Fatalf("list --local: %v: %s", err, out)
	}
	if !strings.Contains(out, kbdID) {
		t.Errorf("local list missing %s:\n%s", kbdID, out)
	}
}

func TestAttachDetach(t *testing.T) {
	p := sharedPair(t)
	resetState(t, p)
	plugKbd(t, p, "kbd0", 1)

	// forward attach runs on imp (importer), targeting dev (exporter)
	pg := p.imp.startBg(t, "usbip-ssh -v attach root@"+devIP+" "+kbdID, "/tmp/attach.log")
	t.Cleanup(func() { p.imp.killBg(pg) })
	mustWait(t, "keyboard to attach via vhci", 60*time.Second, attachedKbds(p, 1))

	if out, err := p.imp.ssh("usbip-ssh detach all"); err != nil {
		t.Fatalf("detach all: %v: %s", err, out)
	}
	mustWait(t, "keyboard to rebind to its usb driver", 60*time.Second, releasedKbds(p, 1))
}

func TestUnbind(t *testing.T) {
	p := sharedPair(t)
	resetState(t, p)
	plugKbd(t, p, "kbd0", 1)

	pg := p.imp.startBg(t, "usbip-ssh -v attach root@"+devIP+" "+kbdID, "/tmp/attach.log")
	t.Cleanup(func() { p.imp.killBg(pg) })
	mustWait(t, "keyboard to attach via vhci", 60*time.Second, attachedKbds(p, 1))

	// forward unbind: imp ssh'es to dev and unbinds the exporter there
	if out, err := p.imp.ssh("usbip-ssh -v unbind root@" + devIP + " " + kbdID); err != nil {
		t.Fatalf("unbind: %v: %s", err, out)
	}
	mustWait(t, "keyboard to return to its usb driver", 60*time.Second, releasedKbds(p, 1))
}

func TestVhubHotAttach(t *testing.T) {
	p := sharedPair(t)
	resetState(t, p)
	plugKbd(t, p, "kbd0", 1)

	pg := p.imp.startBg(t, "usbip-ssh -v attach --vhub root@"+devIP+" "+kbdID, "/tmp/vhub.log")
	t.Cleanup(func() { p.imp.killBg(pg) })
	mustWait(t, "first keyboard to attach", 60*time.Second, attachedKbds(p, 1))

	// hot-plug a second matching keyboard on dev while --vhub is watching
	if err := p.dev.qmp.deviceAdd("usb-kbd", "kbd1"); err != nil {
		t.Fatalf("device_add kbd1: %v", err)
	}
	mustWait(t, "second keyboard to hot-attach", 60*time.Second, attachedKbds(p, 2))

	// killing the whole session must release and rebind both devices
	p.imp.killBg(pg)
	mustWait(t, "both keyboards to rebind after the session dies", 60*time.Second, releasedKbds(p, 2))
}

func TestKeepReconnect(t *testing.T) {
	p := sharedPair(t)
	resetState(t, p)
	plugKbd(t, p, "kbd0", 1)

	pg := p.imp.startBg(t, "usbip-ssh -v keep root@"+devIP+" "+kbdID, "/tmp/keep.log")
	t.Cleanup(func() { p.imp.killBg(pg) })
	mustWait(t, "keyboard to attach", 60*time.Second, attachedKbds(p, 1))

	// unplug: the connection collapses and keep enters its retry loop
	if err := p.dev.qmp.deviceDel("kbd0"); err != nil {
		t.Fatalf("device_del kbd0: %v", err)
	}
	mustWait(t, "keyboard to disappear", 60*time.Second, noKbds(p))

	// replug: keep's backoff loop must re-attach without being restarted
	if err := p.dev.qmp.deviceAdd("usb-kbd", "kbd0"); err != nil {
		t.Fatalf("device_add kbd0: %v", err)
	}
	mustWait(t, "keep to re-attach the keyboard", 3*time.Minute, attachedKbds(p, 1))
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
