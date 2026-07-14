package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const minWait, maxWait = 0.5, 60.0

// sleepAfter returns how long to sleep after a run that took dt seconds.
func sleepAfter(wait, dt float64) float64 {
	if dt >= wait {
		return 0
	}
	if dt < 1 {
		dt = 1
	}
	return wait / dt
}

// nextWait returns the updated backoff after a run that took dt seconds.
func nextWait(wait, dt float64) float64 {
	if dt > maxWait {
		return minWait
	}
	if wait *= 4; wait > maxWait {
		return maxWait
	}
	return wait
}

// persistent runs f forever, spacing retries out the longer it keeps
// failing quickly, and resetting the backoff after a long good run. It stops
// once a shutdown signal has been caught (reverse mode, between sessions).
func persistent(f func() error) {
	for wait := minWait; !shuttingDown.Load(); {
		start := time.Now()
		xeval(f)
		dt := time.Since(start).Seconds()
		deb("done after %g seconds", dt)
		if st := sleepAfter(wait, dt); st > 0 {
			deb("will sleep for %g seconds", st)
			time.Sleep(time.Duration(st * float64(time.Second)))
		}
		wait = nextWait(wait, dt)
	}
}

var keepSSHOpts = []string{
	"-oConnectTimeout=15", "-oServerAliveInterval=15",
	"-oCheckHostIP=no", "-oBatchMode=yes",
}

func keepCmd(host, pattern string, o attachOpts) {
	sshCmd = append(sshCmd, keepSSHOpts...)
	persistent(func() error {
		_, err := runSession(host, pattern, o)
		return err
	})
}

const daemonEnv = "USBIP_SSH_DAEMONIZED"

// daemonCmd re-execs itself detached from the tty (Go cannot fork), then
// runs the keep loop with syslog logging.
func daemonCmd(host, pattern string, o attachOpts) {
	if os.Getenv(daemonEnv) == "" {
		devnull, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
		if err != nil {
			fatalf("%s", err)
		}
		cmd := &exec.Cmd{
			Path:        "/proc/self/exe",
			Args:        os.Args,
			Env:         append(os.Environ(), daemonEnv+"=1"),
			Stdin:       devnull,
			Stdout:      devnull,
			Stderr:      devnull,
			SysProcAttr: &syscall.SysProcAttr{Setsid: true},
		}
		if err := cmd.Start(); err != nil {
			fatalf("daemonize: %s", err)
		}
		cmd.Process.Release()
		return // parent exits
	}
	useSyslog()
	sshCmd = append(sshCmd, "-y") // ssh logs to syslog too
	keepCmd(host, pattern, o)
}

// useSyslog switches logging to syslog(3) via /dev/log.
func useSyslog() {
	conn, err := net.Dial("unixgram", "/dev/log")
	if err != nil {
		return // keep logging to stderr
	}
	logf = func(pri int, msg string) {
		fmt.Fprintf(conn, "<%d>%s %s: %s", logDaemon|pri,
			time.Now().Format(time.Stamp), progName,
			strings.TrimRight(msg, " \t\n"))
	}
}
