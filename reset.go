package main

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// usbdevfsReset is USBDEVFS_RESET, _IO('U', 20), as per
// linux/usbdevice_fs.h; x/sys/unix does not define it.
const usbdevfsReset = 0x5514

// devbususb is where usbfs device nodes live; a var so tests can fake it.
var devbususb = "/dev/bus/usb"

// resetMatching port-resets every device matching pat, like usbreset(1).
func resetMatching(pat *devPattern) error {
	devs, err := findDev(pat, 1, false)
	if err != nil {
		return err
	}
	for _, busid := range devs {
		if err := resetDevice(busid); err != nil {
			return err
		}
	}
	return nil
}

// resetDevice issues USBDEVFS_RESET on busid's /dev/bus/usb node.
func resetDevice(busid string) error {
	var v [2]int
	for i, f := range []string{"busnum", "devnum"} {
		s, err := xreadFile(devices() + "/" + busid + "/" + f)
		if err != nil {
			return err
		}
		if v[i], err = strconv.Atoi(s); err != nil {
			return fmt.Errorf("%s of %s: %w", f, busid, err)
		}
	}
	node := fmt.Sprintf("%s/%03d/%03d", devbususb, v[0], v[1])
	f, err := os.OpenFile(node, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	deb("IOCTL %s USBDEVFS_RESET", node)
	if _, err := unix.IoctlRetInt(int(f.Fd()), usbdevfsReset); err != nil {
		return fmt.Errorf("reset %s (%s): %w", busid, node, err)
	}
	return nil
}
