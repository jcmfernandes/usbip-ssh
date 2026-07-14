package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// payloadFor is payloadBytes behind a var so tests can inject a payload.
var payloadFor = payloadBytes

// bootstrap runs on the remote host under its login shell. It reports the
// architecture, then reads the payload size, the JSON argument line and
// the payload itself from stdin, and finally execs the payload — which
// inherits the ssh channel as stdin/stdout.
const bootstrap = `sh -c 'p=$(mktemp) && printf "%s\n" "-Arch $(uname -m)" && read -r n && read -r args && head -c "$n" >"$p" && chmod +x "$p" && exec "$p" remote "$args"'`

type remoteSession struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	pr  *os.File       // read end of the merged output pipe
	out *bufio.Scanner // merged remote stdout+stderr
}

func (s *remoteSession) kill() {
	if s.cmd.Process != nil {
		deb("kill %d", s.cmd.Process.Pid)
		s.cmd.Process.Signal(syscall.SIGTERM)
	}
}

// waitExit reaps cmd; a death by signal n maps to exit code n|64.
func waitExit(cmd *exec.Cmd) int {
	err := cmd.Wait()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return int(ws.Signal()) | 64
		}
		return ee.ExitCode()
	}
	return 1
}

// copyOutput re-logs a line of remote output. ERROR:/WARNING: lines keep
// their level; anything else is debug chatter shown only when verbose.
func copyOutput(line string) {
	rest, pri := line, logDebug
	if r, ok := strings.CutPrefix(line, "ERROR:"); ok {
		rest, pri = r, logErr
	} else if r, ok := strings.CutPrefix(line, "WARNING:"); ok {
		rest, pri = r, logWarning
	} else if !verbose {
		return
	}
	logf(pri, "    "+rest+"\n")
}

// startRemote spawns ssh (with sshArgs added) running the bootstrap on
// host, performs the -Arch handshake and ships ra plus the payload.
func startRemote(sshArgs []string, host string, ra remoteArgs) (*remoteSession, error) {
	argv := append(append([]string{}, sshCmd...), sshArgs...)
	argv = append(argv, host, bootstrap)
	deb("EXEC %s", strings.Join(argv, " "))
	cmd := exec.Command(argv[0], argv[1:]...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw // ssh's own messages become protocol chatter
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	pw.Close()
	handshake := false
	defer func() {
		if !handshake {
			pr.Close()
		}
	}()
	s := &remoteSession{cmd: cmd, in: in, pr: pr, out: bufio.NewScanner(pr)}
	var chatter []string
	for s.out.Scan() {
		line := s.out.Text()
		arch, ok := strings.CutPrefix(line, "-Arch ")
		if !ok {
			copyOutput(line)
			if len(chatter) == 5 {
				chatter = chatter[1:]
			}
			chatter = append(chatter, line)
			continue
		}
		payload := payloadFor(strings.TrimSpace(arch))
		if len(payload) == 0 {
			s.kill()
			cmd.Wait()
			return nil, fmt.Errorf("no payload for remote architecture %q"+
				" (unsupported arch, or a payload-tagged build)", arch)
		}
		blob, err := json.Marshal(ra)
		if err != nil {
			s.kill()
			cmd.Wait()
			return nil, err
		}
		if _, err := fmt.Fprintf(in, "%d\n%s\n", len(payload), blob); err == nil {
			_, err = in.Write(payload)
		}
		if err != nil {
			s.kill()
			cmd.Wait()
			return nil, fmt.Errorf("shipping payload: %w", err)
		}
		handshake = true
		return s, nil
	}
	if err := s.out.Err(); err != nil {
		s.kill()
		cmd.Wait()
		return nil, fmt.Errorf("reading remote output: %w", err)
	}
	cmd.Wait()
	msg := "remote bootstrap failed (no -Arch line)"
	if len(chatter) > 0 {
		msg += ":\n    " + strings.Join(chatter, "\n    ")
	}
	return nil, errors.New(msg)
}

// runAttach performs one attach session against host and returns ssh's
// exit code once the session ends.
func runAttach(host, pattern string, o attachOpts) (int, error) {
	tmpdir, err := os.MkdirTemp("", progName+"-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmpdir)
	ctl, lpath := tmpdir+"/ctl", tmpdir+"/vhci"
	ra := remoteArgs{Op: "attach", Pattern: pattern, Vhub: o.vhub,
		NoUnmount: o.noUnmount, NoLinger: o.noLinger, Verbose: verbose}
	s, err := startRemote([]string{"-S", ctl, "-M"}, host, ra)
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
loop:
	for s.out.Scan() {
		line := s.out.Text()
		switch {
		case strings.HasPrefix(line, "-Socket "):
			rpath := strings.TrimPrefix(line, "-Socket ")
			if err := xsystem(sshCmd[0], "-S", ctl, "-O", "forward",
				"-L", lpath+":"+rpath, host); err != nil {
				return 0, err
			}
			if err := xsystem(sshCmd[0], "-S", ctl, "-q", "-O", "stop", host); err != nil {
				return 0, err
			}
		case strings.HasPrefix(line, "-Dev "):
			if err := attachFromLine(lpath, strings.TrimPrefix(line, "-Dev ")); err != nil {
				return 0, err
			}
		case line == "-Eof":
			break loop
		default:
			copyOutput(line)
		}
	}
	if err := s.out.Err(); err != nil {
		return 0, fmt.Errorf("reading remote output: %w", err)
	}
	done = true
	return waitExit(s.cmd), nil
}

// attachFromLine handles one "-Dev bus dev speed" line: connect through
// the forwarded unix socket and hand the fd to vhci.
func attachFromLine(lpath, rest string) error {
	f := strings.Fields(rest)
	if len(f) != 3 {
		return fmt.Errorf("bad -Dev line: %s", rest)
	}
	bus, _ := strconv.Atoi(f[0])
	dev, _ := strconv.Atoi(f[1])
	deb("connect to %s", lpath)
	conn, err := net.Dial("unix", lpath)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", lpath, err)
	}
	defer conn.Close()
	file, err := conn.(*net.UnixConn).File()
	if err != nil {
		return err
	}
	defer file.Close()
	return importerAttach(int(file.Fd()), bus, dev, f[2])
}

// remoteSimple runs a remote list/unbind, passing its output through.
func remoteSimple(host string, ra remoteArgs) int {
	s, err := startRemote(nil, host, ra)
	if err != nil {
		fatalf("%s", err)
	}
	defer s.pr.Close()
	for s.out.Scan() {
		fmt.Println(s.out.Text())
	}
	if err := s.out.Err(); err != nil {
		s.kill()
		logf(logErr, "ERROR: reading remote output: "+err.Error()+"\n")
	}
	return waitExit(s.cmd)
}
