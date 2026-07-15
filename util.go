package main

import (
	"fmt"
	"os"
)

const progName = "usbip-ssh"

// version is stamped at build time via -ldflags -X main.version=...
var version = "dev"

// syslog(3) priorities / facility.
const (
	logErr     = 3
	logWarning = 4
	logInfo    = 6
	logDebug   = 7
	logDaemon  = 3 << 3
)

var (
	verbose    bool
	sysfs      = "/sys"
	modprobe   = "modprobe"
	sshCmd     = []string{"ssh"}
	sudo       bool
	sudoPrompt bool
	sudoPass   string
	logf       = func(pri int, msg string) { fmt.Fprint(os.Stderr, msg) }
)

func drivers() string { return sysfs + "/bus/usb/drivers" }
func devices() string { return sysfs + "/bus/usb/devices" }

func deb(format string, a ...any) {
	if verbose {
		logf(logDebug, fmt.Sprintf(format, a...)+"\n")
	}
}

func warnf(format string, a ...any) {
	logf(logWarning, "WARNING: "+fmt.Sprintf(format, a...)+"\n")
}

func fatalf(format string, a ...any) {
	logf(logErr, "ERROR: "+fmt.Sprintf(format, a...)+"\n")
	os.Exit(1)
}

// xeval logs f's error (if any) and carries on, like the Perl xeval.
func xeval(f func() error) {
	if err := f(); err != nil {
		logf(logErr, "ERROR: "+err.Error()+"\n")
	}
}
