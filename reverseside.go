package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// In reverse mode the exporter's linger runs in this process (unlike forward
// mode, where a linger child on the remote survives our death). A SIGINT or
// SIGTERM must therefore rebind the held devices before we exit, rather than
// leaving them stranded on usbip-host. watchSignals wires a once-only handler
// that trips shuttingDown and closes sigPipeW; closing it leaves sigPipeR
// permanently readable, which wakes every reverseLinger poll.
var (
	sigOnce            sync.Once
	sigPipeR, sigPipeW *os.File
	shuttingDown       atomic.Bool
)

func watchSignals() {
	sigOnce.Do(func() {
		var err error
		if sigPipeR, sigPipeW, err = os.Pipe(); err != nil {
			fatalf("%s", err)
		}
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-ch
			shuttingDown.Store(true)
			sigPipeW.Close()
		}()
	})
}

// runReverseAttach performs one reverse-attach session: it exports local
// devices matching pattern to HOST's vhci. Local is the exporter (binds the
// device to usbip-host), HOST is the importer (its payload runs vhci). The
// ssh channel is remote-forwarded with -R, and the local process itself plays
// the linger role in-process, rebinding when the importer or channel dies.
func runReverseAttach(host, pattern string, o attachOpts) (int, error) {
	watchSignals()
	pat := mustPattern(pattern)
	devs, err := findDev(pat, 1, !o.vhub)
	if err != nil {
		return 0, err
	}
	tmpdir, err := os.MkdirTemp("", progName+"-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmpdir)
	ctl, lpath := tmpdir+"/ctl", tmpdir+"/exp"
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: lpath, Net: "unix"})
	if err != nil {
		return 0, fmt.Errorf("listen on %s: %w", lpath, err)
	}
	defer l.Close()

	s, err := startRemote([]string{"-S", ctl, "-M"}, host,
		remoteArgs{Op: "import", Verbose: verbose})
	if err != nil {
		return 0, err
	}
	defer s.pr.Close()
	done := false
	defer func() {
		if !done {
			s.kill()
			s.cmd.Wait()
		}
	}()

	// The payload reports the remote socket path it wants the -R forward to
	// create; wire lpath to it, then stop the master accepting new sessions.
	rpath, err := waitSocket(s)
	if err != nil {
		return 0, err
	}
	if err := xsystem(sshCmd[0], "-S", ctl, "-O", "forward",
		"-R", rpath+":"+lpath, host); err != nil {
		return 0, err
	}
	if err := xsystem(sshCmd[0], "-S", ctl, "-q", "-O", "stop", host); err != nil {
		return 0, err
	}

	var socks []lingerSock
	// exportOne binds busid to usbip-host and hands its socket to the remote
	// vhci: it announces the device to the payload, accepts the connection the
	// payload makes back through the -R forward, and binds the fd locally.
	exportOne := func(busid string) error {
		var v [3]string
		for i, f := range []string{"busnum", "devnum", "speed"} {
			if v[i], err = xreadFile(devices() + "/" + busid + "/" + f); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(s.in, "-Dev %s %s %s\n", v[0], v[1], v[2]); err != nil {
			return fmt.Errorf("announcing device: %w", err)
		}
		l.SetDeadline(time.Now().Add(2 * time.Second))
		conn, err := l.AcceptUnix()
		if err != nil {
			return fmt.Errorf("accept on %s: %w", lpath, err)
		}
		file, err := conn.File()
		conn.Close()
		if err != nil {
			return err
		}
		deb("accept on %s = %d", lpath, file.Fd())
		if err := exporterAttach(int(file.Fd()), busid, !o.noUnmount); err != nil {
			file.Close()
			return err
		}
		if o.noLinger {
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
		if err := exportOne(busid); err != nil {
			return 0, err
		}
	}

	// In oneshot mode the device set is complete, so let the payload exit
	// (like forward mode's -Eof): its session ending is what lets the ssh
	// master exit when the forwarded connection later drops, which is how we
	// notice a detach/unplug/disconnect. --vhub keeps the payload alive to
	// receive devices that appear later.
	if !o.vhub {
		if _, err := fmt.Fprintln(s.in, "-Eof"); err != nil {
			return 0, fmt.Errorf("signaling end of devices: %w", err)
		}
	}

	reverseLinger(s, pat, &socks, o.vhub, exportOne)
	// The session is over: make sure the master is gone (on a dead channel it
	// already is; on an internal error it may still be running) so waitExit
	// does not block, then report its status.
	s.kill()
	done = true
	return waitExit(s.cmd), nil
}

// waitSocket reads the payload's output until its "-Socket PATH" line,
// passing any earlier chatter through. PATH is where the -R forward's remote
// listening socket should be created.
func waitSocket(s *remoteSession) (string, error) {
	for s.out.Scan() {
		line := s.out.Text()
		if rpath, ok := strings.CutPrefix(line, "-Socket "); ok {
			return rpath, nil
		}
		copyOutput(line)
	}
	if err := s.out.Err(); err != nil {
		return "", fmt.Errorf("reading remote output: %w", err)
	}
	return "", fmt.Errorf("remote import failed (no -Socket line)")
}

// reverseLinger holds the exported devices for the life of the session. It is
// the local mirror of vhubLoop: it polls the ssh channel (via a self-pipe fed
// by a goroutine draining the payload's output), the uevent socket in --vhub
// mode, and every held exporter socket. A dead channel rebinds everything and
// ends the session; a single dead socket rebinds just that device.
func reverseLinger(s *remoteSession, pat *devPattern, socks *[]lingerSock, vhub bool, exportOne func(string) error) {
	// Drain the payload's output in the background; its EOF means the ssh
	// channel is gone. Relay that to the poll loop through a self-pipe.
	deadR, deadW, err := os.Pipe()
	if err != nil {
		warnf("pipe: %s", err)
		rebindAll(*socks)
		return
	}
	defer deadR.Close()
	go func() {
		for s.out.Scan() {
			copyOutput(s.out.Text())
		}
		deadW.Close()
	}()

	ueFd := -1
	if vhub {
		if ueFd, err = ueventSocket(); err != nil {
			warnf("%s", err)
			rebindAll(*socks)
			return
		}
	}
	buf := make([]byte, 65536)
	for {
		// Poll set: [0] dead channel, [1] signal, [ueIdx] uevents (--vhub),
		// then one entry per held exporter socket.
		pfds := []unix.PollFd{
			{Fd: int32(deadR.Fd()), Events: unix.POLLIN},
			{Fd: int32(sigPipeR.Fd()), Events: unix.POLLIN},
		}
		ueIdx := -1
		if vhub {
			ueIdx = len(pfds)
			pfds = append(pfds, unix.PollFd{Fd: int32(ueFd), Events: unix.POLLIN})
		}
		base := len(pfds)
		for _, sk := range *socks {
			pfds = append(pfds, unix.PollFd{Fd: int32(sk.file.Fd()), Events: pollRDHUP})
		}
		if _, err := unix.Poll(pfds, -1); err != nil {
			if err == unix.EINTR {
				continue
			}
			warnf("poll: %s", err)
			rebindAll(*socks)
			return
		}
		// A signal means we are shutting down for good: rebind and exit the
		// process so `keep` does not reconnect.
		if pfds[1].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
			rebindAll(*socks)
			os.Exit(0)
		}
		// A dead channel ends this session; `keep` may then reconnect.
		if pfds[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
			rebindAll(*socks)
			return
		}
		var keep []lingerSock
		for i, sk := range *socks {
			if pfds[base+i].Revents&(pollRDHUP|unix.POLLHUP|unix.POLLERR) != 0 {
				busid := sk.busid
				xeval(func() error { return exporterDetach(busid) })
				sk.file.Close()
			} else {
				keep = append(keep, sk)
			}
		}
		*socks = keep
		if !vhub {
			continue
		}
		if pfds[ueIdx].Revents&unix.POLLIN == 0 {
			continue
		}
		n, _, err := unix.Recvfrom(ueFd, buf, unix.MSG_DONTWAIT)
		if err != nil {
			if err == unix.ENOBUFS || err == unix.EINTR || err == unix.EAGAIN {
				continue
			}
			warnf("recv(kobject_uevent): %s", err)
			rebindAll(*socks)
			return
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
		if err := exportOne(filepath.Base(p)); err != nil {
			warnf("%s", err)
		}
	}
}

// rebindAll releases every held exporter socket back to its normal driver.
func rebindAll(socks []lingerSock) {
	for _, sk := range socks {
		busid := sk.busid
		xeval(func() error { return exporterDetach(busid) })
		sk.file.Close()
	}
}

// reverseUnbind releases local devices matching pattern from usbip-host back
// to their normal drivers. It is the local exporter-side mirror of the remote
// "unbind" op and runs without any ssh connection.
func reverseUnbind(pattern string) int {
	if err := unbindMatching(mustPattern(pattern)); err != nil {
		fatalf("%s", err)
	}
	return 0
}
