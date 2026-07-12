package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// speed codes as per linux/usb/ch9.h, sysfs status values as per
// drivers/usb/usbip/usbip_common.h
const (
	speedLow       = 1
	speedFull      = 2
	speedHigh      = 3
	speedSuper     = 5
	speedSuperPlus = 6

	sdevStAvailable = 1
	sdevStUsed      = 2
	vdevStNull      = 4
	vdevStUsed      = 6

	pollRDHUP = 0x2000 // Linux-only POLLRDHUP
)

// speedCode maps a sysfs speed string (Mbps) to a vhci speed code,
// as per linux/usb/ch9.h and drivers/usb/core/sysfs.c
func speedCode(s string) int {
	switch s {
	case "1.5":
		return speedLow
	case "12":
		return speedFull
	case "480":
		return speedHigh
	case "5000":
		return speedSuper
	case "10000", "20000":
		return speedSuperPlus
	}
	return speedHigh
}

// statusLines returns a vhci status file's lines minus the header.
func statusLines(path string) []string {
	lines := strings.Split(readFile(path), "\n")
	if len(lines) <= 1 {
		return nil
	}
	return lines[1:]
}

func vhciStatusFiles() []string {
	files, _ := filepath.Glob(sysfs + "/devices/platform/vhci_hcd.*/status*")
	return files
}

// findVhciAndPort picks a free vhci port on the right hub type for the
// given speed, loading vhci-hcd if needed.
func findVhciAndPort(speed int) (string, int, error) {
	shub := "hs"
	if speed >= speedSuper {
		shub = "ss"
	}
	if g, _ := filepath.Glob(sysfs + "/devices/platform/vhci_hcd.*"); len(g) == 0 {
		if err := xsystem(modprobe, "vhci-hcd"); err != nil {
			return "", 0, err
		}
	}
	for _, f := range vhciStatusFiles() {
		for _, line := range statusLines(f) {
			fl := strings.Fields(line)
			if len(fl) < 3 {
				continue
			}
			sta, _ := strconv.Atoi(fl[2])
			if sta == vdevStNull && fl[0] == shub {
				port, _ := strconv.Atoi(fl[1])
				return filepath.Dir(f), port, nil
			}
		}
	}
	return "", 0, fmt.Errorf("no suitable vhci port found for speed = %d", speed)
}

func localAttach(sockfd, bus, dev int, speedStr string) error {
	hspeed := speedCode(speedStr)
	vhci, port, err := findVhciAndPort(hspeed)
	if err != nil {
		return err
	}
	return xwriteFile(vhci+"/attach",
		fmt.Sprintf("%d %d %d %d", port, sockfd, bus<<16|dev, hspeed))
}

func localDetach(args []string) error {
	want := map[string]bool{}
	for _, a := range args {
		want[a] = true
	}
	var attached []string
	n := 0
	for _, f := range vhciStatusFiles() {
		for _, line := range statusLines(f) {
			fl := strings.Fields(line)
			if len(fl) < 7 {
				continue
			}
			sta, _ := strconv.Atoi(fl[2])
			if sta != vdevStUsed {
				continue
			}
			busid := fl[6]
			attached = append(attached, busid)
			if !want["all"] && !want[busid] {
				continue
			}
			port, _ := strconv.Atoi(fl[1])
			if err := xwriteFile(filepath.Dir(f)+"/detach", strconv.Itoa(port)); err != nil {
				return err
			}
			n++
		}
	}
	if len(attached) == 0 {
		return errors.New("no devices attached")
	}
	if n == 0 {
		return fmt.Errorf("'%s' did not match any of: %s",
			strings.Join(args, " "), strings.Join(attached, " "))
	}
	return nil
}

func remoteAttach(sockfd int, busid string, unmount bool) error {
	if readFile(devices()+"/"+busid+"/bDeviceClass") == "09" {
		return fmt.Errorf("%s is a hub, and usbip-host cannot attach to a hub", busid)
	}
	status, _ := strconv.Atoi(readFile(drivers() + "/usbip-host/" + busid + "/usbip_status"))
	if status == sdevStUsed {
		if err := xwriteFile(devices()+"/"+busid+"/usbip_sockfd", "-1"); err != nil {
			return err
		}
	} else if status != sdevStAvailable {
		if _, err := os.Stat(drivers() + "/usbip-host"); err != nil {
			if err := xsystem(modprobe, "usbip-host"); err != nil {
				return err
			}
		}
		if _, err := os.Lstat(devices() + "/" + busid + "/driver"); err == nil {
			if unmount {
				if err := doUnmounts(busid); err != nil {
					return err
				}
			}
			if err := xwriteFile(devices()+"/"+busid+"/driver/unbind", busid); err != nil {
				return err
			}
		}
		if err := xwriteFile(drivers()+"/usbip-host/match_busid", "add "+busid); err != nil {
			return err
		}
		if err := xwriteFile(drivers()+"/usbip-host/bind", busid); err != nil {
			return err
		}
	}
	return xwriteFile(devices()+"/"+busid+"/usbip_sockfd", strconv.Itoa(sockfd))
}

func remoteDetach(busid string) error {
	if err := xwriteFile(drivers()+"/usbip-host/unbind", busid); err != nil {
		return err
	}
	if err := xwriteFile(drivers()+"/usbip-host/match_busid", "del "+busid); err != nil {
		return err
	}
	return xwriteFile(drivers()+"/usbip-host/rebind", busid)
}

var octalEsc = regexp.MustCompile(`\\[0-7]{3}`)

// unescapeOctal decodes mountinfo's \ooo escapes (e.g. \040 for space).
func unescapeOctal(s string) string {
	return octalEsc.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.ParseUint(m[1:], 8, 8)
		return string([]byte{byte(n)})
	})
}

// mountpointsFor returns the mountpoints (last mounted first) whose
// major:minor device number (mountinfo field 3) is in devnums.
func mountpointsFor(mountinfo string, devnums map[string]bool) []string {
	var dirs []string
	lines := strings.Split(strings.TrimRight(mountinfo, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		d := strings.Fields(lines[i])
		if len(d) < 5 {
			continue
		}
		if devnums[d[2]] {
			deb("%s %s %s %s", d[len(d)-2], d[4], d[2], d[len(d)-3])
			dirs = append(dirs, unescapeOctal(d[4]))
		}
	}
	return dirs
}

// doUnmounts unmounts every filesystem backed by one of busid's block
// devices (so that usbip can unbind the disk driver cleanly).
func doUnmounts(busid string) error {
	devnums := map[string]bool{}
	findFiles(devices()+"/"+busid, "dev", func(f string) {
		if d := readFile(f); d != "" {
			devnums[d] = true
		}
	})
	if len(devnums) == 0 {
		return nil
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("open /proc/self/mountinfo: %w", err)
	}
	if dirs := mountpointsFor(string(data), devnums); len(dirs) > 0 {
		return xsystem(append([]string{"umount", "--"}, dirs...)...)
	}
	return nil
}
