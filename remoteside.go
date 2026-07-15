package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type lingerSock struct {
	file  *os.File
	busid string
}

// chownForSSHD hands path to the user that invoked us through sudo (--sudo /
// --sudo-prompt), if any. The payload runs as root there, but the ssh forwards
// into our temp dir are still made by sshd as the *login* user: it creates the
// -R listen socket and connects to the -L target. Without this it cannot get
// into our root-owned dir and the forward fails. Without sudo (a root login)
// there is no SUDO_UID and this is a no-op.
func chownForSSHD(path string) error {
	su, sg := os.Getenv("SUDO_UID"), os.Getenv("SUDO_GID")
	if su == "" || sg == "" {
		return nil
	}
	uid, err := strconv.Atoi(su)
	if err != nil {
		return fmt.Errorf("SUDO_UID %q: %w", su, err)
	}
	gid, err := strconv.Atoi(sg)
	if err != nil {
		return fmt.Errorf("SUDO_GID %q: %w", sg, err)
	}
	return os.Chown(path, uid, gid)
}

// remoteTmpdir is the socket directory created by remoteScript, if any; it
// must be removed on every exit path (oneshot -Eof, linger handoff, or a
// fatal error), not just the happy oneshot path.
var remoteTmpdir string

func remoteCleanup() {
	if remoteTmpdir != "" {
		os.RemoveAll(remoteTmpdir)
	}
}

// remoteFatalf cleans up remoteTmpdir before reporting a fatal error, so
// paths that already created the tmpdir (remoteScript, vhubLoop) don't leak
// it on the way out.
func remoteFatalf(format string, a ...any) {
	remoteCleanup()
	fatalf(format, a...)
}

// remoteMain is the payload's entry point on the remote host. Its stdin
// and stdout are the ssh channel; jsonArg carries the remoteArgs.
func remoteMain(jsonArg string) {
	os.Remove(os.Args[0]) // the mktemp file the bootstrap wrote us to
	unix.Dup2(1, 2)       // merge stderr into stdout for the local side
	signal.Ignore(syscall.SIGHUP, syscall.SIGPIPE)
	var ra remoteArgs
	if err := json.Unmarshal([]byte(jsonArg), &ra); err != nil {
		fatalf("bad remote args %q: %s", jsonArg, err)
	}
	verbose = ra.Verbose
	switch ra.Op {
	case "list":
		if err := listDevices(mustPattern(ra.Pattern)); err != nil {
			fatalf("%s", err)
		}
	case "unbind":
		if err := unbindMatching(mustPattern(ra.Pattern)); err != nil {
			fatalf("%s", err)
		}
	case "attach":
		remoteScript(ra)
	case "import":
		remoteImport(ra)
	case "detach":
		if err := importerDetach(strings.Fields(ra.Pattern)); err != nil {
			fatalf("%s", err)
		}
	default:
		fatalf("bad remote op %q", ra.Op)
	}
}

func remoteScript(ra remoteArgs) {
	pat := mustPattern(ra.Pattern)
	devs, err := findDev(pat, 1, !ra.Vhub)
	if err != nil {
		remoteFatalf("%s", err)
	}
	tmpdir, err := os.MkdirTemp("", progName+"-")
	if err != nil {
		remoteFatalf("%s", err)
	}
	remoteTmpdir = tmpdir
	if err := chownForSSHD(tmpdir); err != nil {
		remoteFatalf("%s", err)
	}
	rpath := tmpdir + "/host"
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: rpath, Net: "unix"})
	if err != nil {
		remoteFatalf("listen on %s: %s", rpath, err)
	}
	if err := chownForSSHD(rpath); err != nil {
		remoteFatalf("%s", err)
	}
	fmt.Printf("-Socket %s\n", rpath)

	var socks []lingerSock
	newDev := func(busid, bus, dev, speed string) error {
		fmt.Printf("-Dev %s %s %s\n", bus, dev, speed)
		l.SetDeadline(time.Now().Add(2 * time.Second))
		conn, err := l.AcceptUnix()
		if err != nil {
			return fmt.Errorf("accept on %s: %w", rpath, err)
		}
		file, err := conn.File()
		conn.Close()
		if err != nil {
			return err
		}
		deb("accept on %s = %d", rpath, file.Fd())
		if err := exporterAttach(int(file.Fd()), busid, !ra.NoUnmount); err != nil {
			file.Close()
			return err
		}
		if ra.NoLinger {
			file.Close()
		} else {
			socks = append(socks, lingerSock{file: file, busid: busid})
		}
		return nil
	}

	for _, busid := range devs {
		if readFile(devices()+"/"+busid+"/bDeviceClass") == "09" {
			continue // a hub
		}
		var v [3]string
		for i, f := range []string{"busnum", "devnum", "speed"} {
			if v[i], err = xreadFile(devices() + "/" + busid + "/" + f); err != nil {
				remoteFatalf("%s", err)
			}
		}
		if err := newDev(busid, v[0], v[1], v[2]); err != nil {
			remoteFatalf("%s", err)
		}
	}

	if !ra.Vhub {
		fmt.Println("-Eof")
		remoteCleanup()
		spawnLingerAndExit(socks) // exits; sshd sees our session end
	}
	vhubLoop(pat, &socks, newDev)
}

// remoteImport is the payload's importer side for reverse attach. The local
// exporter drives: it creates the -R forward to rpath and announces each
// device with a "-Dev bus dev speed" line on our stdin. For each, we connect
// back through the forward and hand the resulting fd to vhci. The loop ends
// when stdin closes (the ssh channel is gone), at which point vhci tears the
// ports down on its own.
func remoteImport(ra remoteArgs) {
	tmpdir, err := os.MkdirTemp("", progName+"-")
	if err != nil {
		remoteFatalf("%s", err)
	}
	remoteTmpdir = tmpdir
	if err := chownForSSHD(tmpdir); err != nil {
		remoteFatalf("%s", err)
	}
	rpath := tmpdir + "/imp"
	fmt.Printf("-Socket %s\n", rpath)

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := sc.Text()
		if line == "-Eof" {
			break // oneshot: the device set is complete, so we can exit
		}
		rest, ok := strings.CutPrefix(line, "-Dev ")
		if !ok {
			deb("ignoring %q", line)
			continue
		}
		f := strings.Fields(rest)
		if len(f) != 3 {
			remoteFatalf("bad -Dev line: %s", rest)
		}
		bus, _ := strconv.Atoi(f[0])
		dev, _ := strconv.Atoi(f[1])
		deb("connect to %s", rpath)
		conn, err := net.Dial("unix", rpath)
		if err != nil {
			remoteFatalf("connect to %s: %s", rpath, err)
		}
		file, err := conn.(*net.UnixConn).File()
		conn.Close()
		if err != nil {
			remoteFatalf("%s", err)
		}
		if err := importerAttach(int(file.Fd()), bus, dev, f[2]); err != nil {
			file.Close()
			remoteFatalf("%s", err)
		}
		file.Close() // vhci holds its own reference to the socket
	}
	if err := sc.Err(); err != nil {
		remoteFatalf("reading stdin: %s", err)
	}
	remoteCleanup()
}

// vhubLoop monitors uevents and attaches matching devices as they are
// bound; when the ssh channel dies it hands the sockets to a linger child.
func vhubLoop(pat *devPattern, socks *[]lingerSock, newDev func(busid, bus, dev, speed string) error) {
	ueFd, err := ueventSocket()
	if err != nil {
		remoteFatalf("%s", err)
	}
	buf := make([]byte, 65536)
	for {
		pfds := make([]unix.PollFd, 0, 2+len(*socks))
		pfds = append(pfds,
			unix.PollFd{Fd: 1}, // POLLERR/POLLHUP are always reported
			unix.PollFd{Fd: int32(ueFd), Events: unix.POLLIN})
		for _, s := range *socks {
			pfds = append(pfds, unix.PollFd{Fd: int32(s.file.Fd()), Events: pollRDHUP})
		}
		if _, err := unix.Poll(pfds, -1); err != nil {
			if err == unix.EINTR {
				continue
			}
			remoteFatalf("poll: %s", err)
		}
		// handle the dead-channel condition before everything else, lest
		// we write to a broken pipe below
		if pfds[0].Revents&(unix.POLLERR|unix.POLLHUP) != 0 {
			spawnLingerAndExit(*socks)
		}
		var keep []lingerSock
		for i, s := range *socks {
			if pfds[2+i].Revents&(pollRDHUP|unix.POLLHUP|unix.POLLERR) != 0 {
				busid := s.busid
				xeval(func() error { return exporterDetach(busid) })
				s.file.Close()
			} else {
				keep = append(keep, s)
			}
		}
		*socks = keep
		if pfds[1].Revents&unix.POLLIN == 0 {
			continue
		}
		n, _, err := unix.Recvfrom(ueFd, buf, unix.MSG_DONTWAIT)
		if err != nil {
			if err == unix.ENOBUFS || err == unix.EINTR || err == unix.EAGAIN {
				continue
			}
			remoteFatalf("recv(kobject_uevent): %s", err)
		}
		e := parseUevent(buf[:n])
		if e["ACTION"] != "bind" || e["DEVTYPE"] != "usb_device" ||
			e["DRIVER"] != "usb" || strings.HasPrefix(e["TYPE"], "9/") ||
			strings.Contains(e["DEVPATH"], "vhci_hcd") {
			continue
		}
		p := sysfs + e["DEVPATH"]
		spec := mkDevSpec(p, e)
		deb("UEVENT %s", spec)
		if !pat.match(spec) {
			continue
		}
		speed, err := xreadFile(p + "/speed")
		if err != nil {
			remoteFatalf("%s", err)
		}
		if err := newDev(filepath.Base(p), e["BUSNUM"], e["DEVNUM"], speed); err != nil {
			remoteFatalf("%s", err)
		}
	}
}

func parseUevent(d []byte) map[string]string {
	m := map[string]string{}
	for _, tok := range strings.Split(string(d), "\x00") {
		if k, v, ok := strings.Cut(tok, "="); ok {
			m[k] = v
		}
	}
	return m
}

func ueventSocket() (int, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_DGRAM, unix.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		return -1, fmt.Errorf("socket(kobject_uevent): %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: 1}); err != nil {
		return -1, fmt.Errorf("bind(kobject_uevent): %w", err)
	}
	return fd, nil
}

// spawnLingerAndExit hands the accepted sockets to a fresh child of
// ourselves ("linger" op) so it can rebind the devices when their
// connections die, then exits — Go cannot fork(), and exiting is what
// lets sshd end the session. /proc/self/exe works after self-unlink.
func spawnLingerAndExit(socks []lingerSock) {
	if len(socks) == 0 {
		remoteCleanup()
		os.Exit(0)
	}
	argv := []string{progName, "--sysfs", sysfs, "--modprobe", modprobe}
	if verbose {
		argv = append(argv, "-v")
	}
	argv = append(argv, "linger")
	var files []*os.File
	for _, s := range socks {
		argv = append(argv, s.busid)
		files = append(files, s.file)
	}
	devnull, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		remoteFatalf("%s", err)
	}
	cmd := &exec.Cmd{
		Path:       "/proc/self/exe",
		Args:       argv,
		Stdin:      devnull,
		Stdout:     devnull,
		Stderr:     devnull,
		ExtraFiles: files, // become fds 3, 4, ... in argv order
	}
	if err := cmd.Start(); err != nil {
		remoteFatalf("linger re-exec: %s", err)
	}
	cmd.Process.Release()
	remoteCleanup()
	os.Exit(0)
}

// lingerMain is the linger child: fds 3, 4, ... are the accepted unix
// sockets, in the same order as the busid arguments.
func lingerMain(busids []string) {
	useSyslog()
	socks := make([]lingerSock, len(busids))
	for i, busid := range busids {
		socks[i] = lingerSock{file: os.NewFile(uintptr(3+i), busid), busid: busid}
	}
	lingerLoop(socks)
}

// lingerLoop waits for each socket to hang up, rebinding its device to
// the original driver, and returns when none are left.
func lingerLoop(socks []lingerSock) {
	for len(socks) > 0 {
		pfds := make([]unix.PollFd, len(socks))
		for i, s := range socks {
			pfds[i] = unix.PollFd{Fd: int32(s.file.Fd()), Events: pollRDHUP}
		}
		if _, err := unix.Poll(pfds, -1); err != nil {
			if err == unix.EINTR {
				continue
			}
			fatalf("poll: %s", err)
		}
		var keep []lingerSock
		for i, s := range socks {
			if pfds[i].Revents&(pollRDHUP|unix.POLLHUP|unix.POLLERR) != 0 {
				busid := s.busid
				xeval(func() error { return exporterDetach(busid) })
				s.file.Close()
			} else {
				keep = append(keep, s)
			}
		}
		socks = keep
	}
}
