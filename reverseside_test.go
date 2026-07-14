package main

import (
	"bufio"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// reverseLinger must rebind a device when its importer socket hangs up, and
// must end the session (rebinding whatever is left) when the ssh channel dies.
func TestReverseLinger(t *testing.T) {
	withFixtureSysfs(t)
	mkFile(t, drivers()+"/usbip-host/unbind", "")
	// settleDriver only needs to see *a* driver symlink to conclude the
	// device settled without polling; the target need not resolve.
	if err := os.Symlink("usb", devices()+"/1-1.4/driver"); err != nil {
		t.Fatal(err)
	}

	// pr/pw stand in for the ssh channel: closing pw is the channel dying.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s := &remoteSession{out: bufio.NewScanner(pr)}

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	peer := os.NewFile(uintptr(fds[1]), "peer")
	socks := []lingerSock{{file: os.NewFile(uintptr(fds[0]), "sock"), busid: "1-1.4"}}

	done := make(chan struct{})
	go func() {
		reverseLinger(s, mustPattern("Telink"), &socks, false, nil)
		close(done)
	}()
	peer.Close() // the remote importer went away -> rebind 1-1.4
	pw.Close()   // the ssh channel died -> the session ends
	<-done

	if got := readFile(drivers() + "/usbip-host/rebind"); got != "1-1.4" {
		t.Errorf("rebind = %q, want %q", got, "1-1.4")
	}
}

func TestParseReverse(t *testing.T) {
	rev, rest := parseReverse("detach", []string{"-r", "root@h", "all"})
	if !rev || len(rest) != 2 || rest[0] != "root@h" || rest[1] != "all" {
		t.Errorf("parseReverse(-r) = %v %v", rev, rest)
	}
	rev, rest = parseReverse("detach", []string{"1-1", "2-2"})
	if rev || len(rest) != 2 {
		t.Errorf("parseReverse() = %v %v", rev, rest)
	}
}
