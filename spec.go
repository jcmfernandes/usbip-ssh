package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	busidNameRe = regexp.MustCompile(`^[1-9][\d.-]+$`)
	hubIfRe     = regexp.MustCompile(`\b090000\b`)
)

// mkIfSpec turns a uevent INTERFACE value ("class/subclass/protocol" in
// decimal) into six hex digits.
func mkIfSpec(iface string) string {
	parts := strings.Split(iface, "/")
	if len(parts) != 3 {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		n, _ := strconv.Atoi(p)
		fmt.Fprintf(&b, "%02x", n)
	}
	return b.String()
}

// mkDevSpec builds the one-line device spec that patterns match against
// and list prints:
//
//	busid  vvvv:pppp  BUSNUM/DEVNUM  serial manufacturer product\tifspecs
func mkDevSpec(path string, env map[string]string) string {
	var extra []string
	for _, f := range []string{"serial", "manufacturer", "product"} {
		extra = append(extra, readFile(path+"/"+f))
	}
	var vid, pid int64
	if pp := strings.Split(env["PRODUCT"], "/"); len(pp) >= 2 {
		vid, _ = strconv.ParseInt(pp[0], 16, 64)
		pid, _ = strconv.ParseInt(pp[1], 16, 64)
	}
	busid := filepath.Base(path)
	var ifs []string
	uevents, _ := filepath.Glob(path + "/" + busid + ":*/uevent")
	for _, ue := range uevents {
		ifs = append(ifs, mkIfSpec(readUevent(ue)["INTERFACE"]))
	}
	return fmt.Sprintf("%s  %04x:%04x  %s/%s  %s\t%s",
		busid, vid, pid, env["BUSNUM"], env["DEVNUM"],
		strings.Join(extra, " "), strings.Join(ifs, " "))
}

// findDev returns the busids of the devices matching pat. Hubs never
// match. With single, exactly one match is required; otherwise at least
// min matches.
func findDev(pat *devPattern, min int, single bool) ([]string, error) {
	deb("looking for %s inside %s", pat, devices())
	var found []string
	paths, _ := filepath.Glob(devices() + "/[1-9]*")
	for _, p := range paths {
		busid := filepath.Base(p)
		if !busidNameRe.MatchString(busid) {
			continue
		}
		spec := mkDevSpec(p, readUevent(p+"/uevent"))
		if hubIfRe.MatchString(spec) { // a hub
			continue
		}
		ok := pat.match(spec)
		mark := "  "
		if ok {
			mark = "=>"
			found = append(found, busid)
		}
		deb("  %2s %s", mark, spec)
	}
	if single {
		if len(found) == 1 {
			return found, nil
		}
	} else if len(found) >= min {
		return found, nil
	}
	if len(found) > 0 {
		return nil, fmt.Errorf("multiple devices match '%s'", pat)
	}
	return nil, fmt.Errorf("no devices match '%s'", pat)
}
