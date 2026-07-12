package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// well-known USB class[/subclass[/protocol]] names, keyed by 2/4/6 hex digits
var classNames = map[string]string{
	"01": "audio", "02": "COM",
	"03":     "HID",
	"030001": "kbd",
	"030002": "mouse",
	"030101": "kbd",   // boot
	"030102": "mouse", // boot
	"07":     "printer", "08": "UMASS",
	"09": "hub", "0a": "CDC", "0e": "video",
	"e0":     "wireless",
	"e00101": "bluetooth",
	"e00103": "RNDIS",
	// 'ff' vendor
	"ff4201": "adb",
}

var (
	digitRun   = regexp.MustCompile(`\d+`)
	nonUsbIfRe = regexp.MustCompile(`[^\d/]`)
)

// dvKey builds a sort key where '.' sorts before '-' (by swapping ':' and
// '.') and digit runs compare numerically (as 4-byte big-endian).
func dvKey(s string) string {
	t := strings.Map(func(r rune) rune {
		switch r {
		case ':':
			return '.'
		case '.':
			return ':'
		}
		return r
	}, s)
	return digitRun.ReplaceAllStringFunc(t, func(d string) string {
		n, _ := strconv.ParseUint(d, 10, 32)
		return string([]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
	})
}

func nsort(items []string) {
	sort.Slice(items, func(i, j int) bool { return dvKey(items[i]) < dvKey(items[j]) })
}

func listDevices(pat *devPattern) error {
	entries, err := os.ReadDir(devices())
	if err != nil {
		return fmt.Errorf("opendir %s: %w", devices(), err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	nsort(names)
	for _, name := range names {
		if !busidNameRe.MatchString(name) {
			continue
		}
		path := devices() + "/" + name
		env := readUevent(path + "/uevent")
		indent := strings.Count(name, ".") * 2
		spec := mkDevSpec(path, env)
		if !pat.match(spec) {
			continue
		}
		head, _, _ := strings.Cut(spec, "\t")
		fmt.Printf("%*s%s", indent, "", head)
		if env["DRIVER"] == "usb" {
			fmt.Println()
		} else {
			fmt.Printf("  [%s]\n", env["DRIVER"])
		}
		ifdirs, _ := filepath.Glob(path + "/" + name + ":*")
		for _, ifdir := range ifdirs {
			ie := readUevent(ifdir + "/uevent")
			parts := strings.Split(ie["INTERFACE"], "/")
			if len(parts) != 3 {
				continue
			}
			var hx [3]string
			for i, p := range parts {
				n, _ := strconv.Atoi(p)
				hx[i] = fmt.Sprintf("%02x", n)
			}
			c, s, pr := hx[0], hx[1], hx[2]
			if c == "09" { // skip hub interfaces
				continue
			}
			ifname := classNames[c+s+pr]
			if ifname == "" {
				ifname = classNames[c+s]
			}
			if ifname == "" {
				ifname = classNames[c]
			}
			if ifname == "" {
				if w := readFile(ifdir + "/interface"); w != "" {
					ifname = "/" + strings.Fields(w)[0]
				}
			}
			var extra []string
			if drv := ie["DRIVER"]; drv != "" {
				var d []string
				findFiles(ifdir, "uevent", func(f string) {
					u := readUevent(f)
					if len(u) == 0 {
						return
					}
					if dn, ok := u["DEVNAME"]; ok {
						d = append(d, strings.TrimPrefix(dn, "input/"))
					}
					if rn, ok := u["RFKILL_NAME"]; ok {
						d = append(d, ":"+rn)
					}
					if in, ok := u["INTERFACE"]; ok && nonUsbIfRe.MatchString(in) {
						d = append(d, ":"+in) // a network interface name
					}
				})
				nsort(d)
				extra = append([]string{"[" + drv + "]"}, d...)
			}
			suffix := ifdir[strings.LastIndex(ifdir, ":"):]
			fmt.Printf("%*s    %s %s%s%s %-7s %s\n", indent, "",
				suffix, c, s, pr, ifname, strings.Join(extra, " "))
		}
	}
	return nil
}
