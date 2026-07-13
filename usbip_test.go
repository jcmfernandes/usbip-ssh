package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// mkFile writes a file, creating parent directories (xwriteFile does not).
func mkFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSpeedCode(t *testing.T) {
	cases := map[string]int{
		"1.5": speedLow, "12": speedFull, "480": speedHigh,
		"5000": speedSuper, "10000": speedSuperPlus, "20000": speedSuperPlus,
		"bogus": speedHigh,
	}
	for s, want := range cases {
		if got := speedCode(s); got != want {
			t.Errorf("speedCode(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestUnescapeOctal(t *testing.T) {
	if got := unescapeOctal(`/mnt/my\040disk`); got != "/mnt/my disk" {
		t.Errorf("unescapeOctal = %q", got)
	}
}

func TestMountpointsFor(t *testing.T) {
	mountinfo := `36 25 8:1 / /boot rw - ext4 /dev/sda1 rw
37 25 8:17 / /mnt/usb\040disk rw - vfat /dev/sdb1 rw
38 25 8:17 /sub /mnt/other rw - vfat /dev/sdb1 rw
`
	got := mountpointsFor(mountinfo, map[string]bool{"8:17": true})
	want := []string{"/mnt/other", "/mnt/usb disk"} // reverse order
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mountpointsFor = %v, want %v", got, want)
	}
}

func vhciFixture(t *testing.T) {
	t.Helper()
	old := sysfs
	sysfs = writeTree(t, map[string]string{
		"devices/platform/vhci_hcd.0/status": "hub port sta spd dev sockfd local_busid\n" +
			"hs 0000 004 000 00000000 000000 0-0\n" +
			"hs 0001 006 003 00010004 000005 1-1.4\n",
		"devices/platform/vhci_hcd.0/status.1": "hub port sta spd dev sockfd local_busid\n" +
			"ss 0008 004 000 00000000 000000 0-0\n",
	})
	t.Cleanup(func() { sysfs = old })
}

func TestFindVhciAndPort(t *testing.T) {
	vhciFixture(t)
	dir, port, err := findVhciAndPort(speedHigh)
	if err != nil || port != 0 {
		t.Errorf("findVhciAndPort(hs) = %q, %d, %v", dir, port, err)
	}
	_, port, err = findVhciAndPort(speedSuper)
	if err != nil || port != 8 {
		t.Errorf("findVhciAndPort(ss) = %d, %v", port, err)
	}
}

func TestLocalAttach(t *testing.T) {
	vhciFixture(t)
	if err := localAttach(7, 1, 4, "12"); err != nil {
		t.Fatal(err)
	}
	got := readFile(sysfs + "/devices/platform/vhci_hcd.0/attach")
	want := "0 7 65540 2" // port 0, fd 7, 1<<16|4, speedFull
	if got != want {
		t.Errorf("attach = %q, want %q", got, want)
	}
}

func TestLocalDetach(t *testing.T) {
	vhciFixture(t)
	if err := localDetach([]string{"1-1.4"}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(sysfs + "/devices/platform/vhci_hcd.0/detach"); got != "1" {
		t.Errorf("detach = %q, want %q", got, "1")
	}
	if err := localDetach([]string{"9-9"}); err == nil {
		t.Error("localDetach(9-9) should fail")
	}
}

func TestRemoteAttachAvailable(t *testing.T) {
	withFixtureSysfs(t)
	// SDEV_ST_AVAILABLE: only the sockfd is written
	mkFile(t, drivers()+"/usbip-host/1-1.4/usbip_status", "1")
	if err := remoteAttach(7, "1-1.4", false); err != nil {
		t.Fatal(err)
	}
	if got := readFile(devices() + "/1-1.4/usbip_sockfd"); got != "7" {
		t.Errorf("usbip_sockfd = %q", got)
	}
}

func TestRemoteAttachHub(t *testing.T) {
	withFixtureSysfs(t)
	if err := remoteAttach(7, "1-1", false); err == nil {
		t.Error("remoteAttach on a hub should fail")
	}
}

func TestRemoteAttachUnbound(t *testing.T) {
	withFixtureSysfs(t)
	// no usbip_status yet: device must be bound to usbip-host first;
	// the usbip-host dir exists so modprobe is not attempted
	mkFile(t, drivers()+"/usbip-host/bind", "")
	if err := remoteAttach(7, "1-1.4", false); err != nil {
		t.Fatal(err)
	}
	if got := readFile(drivers() + "/usbip-host/match_busid"); got != "add 1-1.4" {
		t.Errorf("match_busid = %q", got)
	}
	if got := readFile(drivers() + "/usbip-host/bind"); got != "1-1.4" {
		t.Errorf("bind = %q", got)
	}
	if got := readFile(devices() + "/1-1.4/usbip_sockfd"); got != "7" {
		t.Errorf("usbip_sockfd = %q", got)
	}
}

func TestRemoteDetach(t *testing.T) {
	withFixtureSysfs(t)
	mkFile(t, drivers()+"/usbip-host/unbind", "")
	// settleDriver only needs to see *a* driver symlink to conclude the
	// device settled without polling; the target need not resolve.
	if err := os.Symlink("usb", devices()+"/1-1.4/driver"); err != nil {
		t.Fatal(err)
	}
	if err := remoteDetach("1-1.4"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(drivers() + "/usbip-host/unbind"); got != "1-1.4" {
		t.Errorf("unbind = %q", got)
	}
	if got := readFile(drivers() + "/usbip-host/match_busid"); got != "del 1-1.4" {
		t.Errorf("match_busid = %q", got)
	}
	if got := readFile(drivers() + "/usbip-host/rebind"); got != "1-1.4" {
		t.Errorf("rebind = %q", got)
	}
}
