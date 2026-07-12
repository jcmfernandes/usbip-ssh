package main

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestParseUevent(t *testing.T) {
	d := []byte("bind@/devices/pci0/usb1/1-1.4\x00ACTION=bind\x00DEVPATH=/devices/pci0/usb1/1-1.4\x00DEVTYPE=usb_device\x00DRIVER=usb\x00PRODUCT=da/8510/110\x00TYPE=0/0/0\x00BUSNUM=001\x00DEVNUM=004\x00")
	e := parseUevent(d)
	if e["ACTION"] != "bind" || e["DEVPATH"] != "/devices/pci0/usb1/1-1.4" ||
		e["BUSNUM"] != "001" {
		t.Errorf("parseUevent = %v", e)
	}
}

// lingerLoop must detach each device when its socket peer goes away, then
// return once no sockets are left.
func TestLingerLoop(t *testing.T) {
	withFixtureSysfs(t)
	mkFile(t, drivers()+"/usbip-host/unbind", "")
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	peer := os.NewFile(uintptr(fds[1]), "peer")
	done := make(chan struct{})
	go func() {
		lingerLoop([]lingerSock{{fd: fds[0], busid: "1-1.4"}})
		close(done)
	}()
	peer.Close() // hang up
	<-done
	if got := readFile(drivers() + "/usbip-host/rebind"); got != "1-1.4" {
		t.Errorf("rebind = %q, want %q", got, "1-1.4")
	}
}
