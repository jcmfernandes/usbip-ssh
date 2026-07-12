package main

import (
	"strings"
	"testing"
)

// fixtureSysfs builds a fake sysfs with a hub (1-1) and a HID device
// (1-1.4) and returns its root. Callers must assign it to the sysfs global.
func fixtureSysfs(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"bus/usb/devices/usb1/uevent": "DEVTYPE=usb_device\nDRIVER=usb\n",

		"bus/usb/devices/1-1/uevent":       "DEVTYPE=usb_device\nDRIVER=usb\nPRODUCT=2109/3431/426\nBUSNUM=001\nDEVNUM=002\n",
		"bus/usb/devices/1-1/serial":       "0000",
		"bus/usb/devices/1-1/manufacturer": "VIA",
		"bus/usb/devices/1-1/product":      "USB2.0 Hub",
		"bus/usb/devices/1-1/bDeviceClass": "09",
		"bus/usb/devices/1-1/busnum":       "1",
		"bus/usb/devices/1-1/devnum":       "2",
		"bus/usb/devices/1-1/speed":        "480",
		"bus/usb/devices/1-1/1-1:1.0/uevent": "INTERFACE=9/0/0\nDRIVER=hub\n",

		"bus/usb/devices/1-1.4/uevent":       "DEVTYPE=usb_device\nDRIVER=usb\nPRODUCT=da/8510/110\nBUSNUM=001\nDEVNUM=004\n",
		"bus/usb/devices/1-1.4/serial":       "ABC",
		"bus/usb/devices/1-1.4/manufacturer": "Telink",
		"bus/usb/devices/1-1.4/product":      "Wireless Receiver",
		"bus/usb/devices/1-1.4/bDeviceClass": "00",
		"bus/usb/devices/1-1.4/busnum":       "1",
		"bus/usb/devices/1-1.4/devnum":       "4",
		"bus/usb/devices/1-1.4/speed":        "12",
		"bus/usb/devices/1-1.4/1-1.4:1.0/uevent": "INTERFACE=3/1/2\nDRIVER=usbhid\n",
		"bus/usb/devices/1-1.4/1-1.4:1.1/uevent": "INTERFACE=3/1/1\nDRIVER=usbhid\n",

		"bus/usb/devices/1-1.4/1-1.4:1.0/0003:1/input/input5/uevent":        "NAME=mouse\n",
		"bus/usb/devices/1-1.4/1-1.4:1.0/0003:1/input/input5/event4/uevent": "MAJOR=13\nMINOR=68\nDEVNAME=input/event4\n",
		"bus/usb/devices/1-1.4/1-1.4:1.0/0003:1/input/input5/mouse0/uevent": "MAJOR=13\nMINOR=32\nDEVNAME=input/mouse0\n",
		"bus/usb/devices/1-1.4/1-1.4:1.1/0003:2/input/input6/event5/uevent": "MAJOR=13\nMINOR=69\nDEVNAME=input/event5\n",
	}) + "/"
}

func withFixtureSysfs(t *testing.T) {
	t.Helper()
	old := sysfs
	sysfs = strings.TrimSuffix(fixtureSysfs(t), "/")
	t.Cleanup(func() { sysfs = old })
}

func TestMkIfSpec(t *testing.T) {
	if got := mkIfSpec("3/1/2"); got != "030102" {
		t.Errorf("mkIfSpec = %q", got)
	}
	if got := mkIfSpec("224/1/3"); got != "e00103" {
		t.Errorf("mkIfSpec = %q", got)
	}
}

func TestMkDevSpec(t *testing.T) {
	withFixtureSysfs(t)
	path := devices() + "/1-1.4"
	got := mkDevSpec(path, readUevent(path+"/uevent"))
	want := "1-1.4  00da:8510  001/004  ABC Telink Wireless Receiver\t030102 030101"
	if got != want {
		t.Errorf("mkDevSpec\n got %q\nwant %q", got, want)
	}
}

func TestFindDev(t *testing.T) {
	withFixtureSysfs(t)

	got, err := findDev(mustPattern("Telink"), 1, true)
	if err != nil || len(got) != 1 || got[0] != "1-1.4" {
		t.Errorf("findDev(Telink) = %v, %v", got, err)
	}

	// the hub (interface spec 090000) must never match
	if _, err := findDev(mustPattern("3431"), 1, true); err == nil {
		t.Error("findDev should not match the hub")
	}

	if _, err := findDev(mustPattern("nomatch"), 1, true); err == nil ||
		!strings.Contains(err.Error(), "no devices match") {
		t.Errorf("findDev(nomatch) err = %v", err)
	}

	// empty pattern matches the single non-hub device
	got, err = findDev(mustPattern(""), 1, false)
	if err != nil || len(got) != 1 {
		t.Errorf("findDev(\"\") = %v, %v", got, err)
	}
}
