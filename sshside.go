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
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// payloadFor is payloadBytes behind a var so tests can inject a payload.
var payloadFor = payloadBytes

// With --ssh-user the ssh children are dropped to that user so they use that
// user's ssh config, agent and known_hosts, while usbip-ssh itself stays root
// for the usbip sysfs writes. sshCred/sshEnv are applied to every ssh we
// spawn; sshUID/sshGID hand our temp sockets over to it.
var (
	sshUID  int
	sshGID  int
	sshCred *syscall.SysProcAttr
	sshEnv  []string
)

// setupSSHUser resolves --ssh-user into the credential and environment the
// ssh children run under. Only ssh drops privileges, not usbip-ssh itself.
func setupSSHUser(name string) error {
	u, err := user.Lookup(name)
	if err != nil {
		return err
	}
	if sshUID, err = strconv.Atoi(u.Uid); err != nil {
		return fmt.Errorf("uid %q: %w", u.Uid, err)
	}
	if sshGID, err = strconv.Atoi(u.Gid); err != nil {
		return fmt.Errorf("gid %q: %w", u.Gid, err)
	}
	sshCred = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(sshUID), Gid: uint32(sshGID)},
	}
	// ssh finds its config, known_hosts and default agent through these.
	sshEnv = []string{"HOME=" + u.HomeDir, "USER=" + u.Username, "LOGNAME=" + u.Username}
	return nil
}

// applySSHCred makes an ssh invocation run as --ssh-user, if set.
func applySSHCred(cmd *exec.Cmd) {
	if sshCred == nil {
		return
	}
	cmd.SysProcAttr = sshCred
	// exec keeps the last value of a duplicate key, so sshEnv overrides the
	// HOME/USER we inherited from sudo. SSH_AUTH_SOCK passes through as-is.
	cmd.Env = append(os.Environ(), sshEnv...)
}

// chownForSSH hands path to --ssh-user so the ssh client, running as that
// user, can create or connect to the sockets we keep in our temp dir.
func chownForSSH(path string) error {
	if sshCred == nil {
		return nil
	}
	return os.Chown(path, sshUID, sshGID)
}

// sshSystem runs an ssh control-master command (ssh -O ...). It must run as
// --ssh-user too: the mux master checks the connecting peer's uid and refuses
// a mismatch, so a root-run -O forward could not talk to a user-run master.
func sshSystem(argv ...string) error {
	deb("EXEC %s", strings.Join(argv, " "))
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	applySSHCred(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("system %s: %w", strings.Join(argv, " "), err)
	}
	return nil
}

// bootstrap runs on the remote host under its login shell. It reports the
// architecture, then reads the payload size, the JSON argument line and
// the payload itself from stdin, and finally execs the payload — which
// inherits the ssh channel as stdin/stdout.
const bootstrap = `sh -c 'p=$(mktemp) && printf "%s\n" "-Arch $(uname -m)" && read -r n && read -r args && head -c "$n" >"$p" && chmod +x "$p" && exec "$p" remote "$args"'`

// remoteBootstrap is the command ssh runs on HOST: the bootstrap line, wrapped
// in "sudo -n --" when --sudo is set so the payload runs as root even when HOST
// is a non-root user. With --sudo-prompt it is wrapped in "sudo -S -k" with an
// empty prompt instead, reading the password (sent as the first stdin line)
// from the ssh channel. The sudo forms are prefixed with LC_ALL=C so sudo's
// failure messages come out in English and are reliably matched by sudoFailRe.
// Only the payload is wrapped; the ssh control-master calls (-O forward/stop)
// carry no remote command and are left untouched.
func remoteBootstrap() string {
	switch {
	case sudoPrompt:
		return "LC_ALL=C sudo -S -k -p '' -- " + bootstrap
	case sudo:
		return "LC_ALL=C sudo -n -- " + bootstrap
	default:
		return bootstrap
	}
}

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

// errRemoteSudoAuth means the remote sudo rejected the password (or none was
// available). With --sudo-prompt, startRemote turns this into a re-prompt.
var errRemoteSudoAuth = errors.New("remote sudo authentication failed")

// sudoFailRe matches the messages the remote sudo prints on a bad or missing
// password (forced to English via LC_ALL=C in remoteBootstrap). Detecting it
// lets us fail (and re-prompt) promptly instead of leaving sudo blocked on the
// ssh channel waiting for a retry password.
var sudoFailRe = regexp.MustCompile(`(?i)sorry, try again|incorrect password|no password was provided|a password is required|authentication failure`)

// handshakeTimeout bounds the wait for the -Arch line. It is a backstop for the
// case where a wrong --sudo-prompt password makes the remote sudo block on
// stdin but its failure message was localized and missed by sudoFailRe.
const handshakeTimeout = 45 * time.Second

// maxSudoTries caps the interactive --sudo-prompt retries per connection.
const maxSudoTries = 3

// startRemote spawns the remote payload and returns its session. With
// --sudo-prompt it re-prompts for the password when the remote reports an
// authentication failure, so a mistyped password can be corrected in place;
// the corrected password is reused by later connections (keep/reconnect).
func startRemote(sshArgs []string, host string, ra remoteArgs) (*remoteSession, error) {
	for try := 0; ; try++ {
		s, err := dialRemote(sshArgs, host, ra)
		if err == nil {
			return s, nil
		}
		if !errors.Is(err, errRemoteSudoAuth) || !sudoPrompt || try >= maxSudoTries-1 {
			return nil, err
		}
		warnf("remote sudo authentication failed; try again")
		pw, perr := readSudoPassword()
		if perr != nil {
			return nil, err // no terminal to re-prompt on: surface the auth error
		}
		sudoPass = pw
	}
}

// dialRemote makes one connection attempt: runs the bootstrap on host, performs
// the -Arch handshake and ships ra plus the payload. It returns
// errRemoteSudoAuth when the remote sudo rejects the password.
func dialRemote(sshArgs []string, host string, ra remoteArgs) (*remoteSession, error) {
	argv := append(append([]string{}, sshCmd...), sshArgs...)
	argv = append(argv, host, remoteBootstrap())
	deb("EXEC %s", strings.Join(argv, " "))
	cmd := exec.Command(argv[0], argv[1:]...)
	applySSHCred(cmd)
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
	// Bound the handshake: a wrong sudo password leaves the remote sudo
	// blocked reading stdin for a retry, which would otherwise hang us. The
	// deadline is cleared once -Arch arrives so the session reads unbounded.
	pr.SetReadDeadline(time.Now().Add(handshakeTimeout))
	if sudoPrompt {
		// sudo -S reads the password from stdin up to a newline; send it
		// first so sudo consumes it before the bootstrap reads the rest.
		if _, err := io.WriteString(in, sudoPass+"\n"); err != nil {
			s.kill()
			cmd.Wait()
			return nil, fmt.Errorf("sending sudo password: %w", err)
		}
	}
	var chatter []string
	for s.out.Scan() {
		line := s.out.Text()
		arch, ok := strings.CutPrefix(line, "-Arch ")
		if !ok {
			if (sudo || sudoPrompt) && sudoFailRe.MatchString(line) {
				copyOutput(line)
				s.kill()
				cmd.Wait()
				return nil, errRemoteSudoAuth
			}
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
		pr.SetReadDeadline(time.Time{})
		handshake = true
		return s, nil
	}
	scanErr := s.out.Err()
	if scanErr != nil {
		s.kill()
	}
	cmd.Wait()
	if errors.Is(scanErr, os.ErrDeadlineExceeded) {
		// A stalled handshake under --sudo-prompt almost always means sudo is
		// blocked on a password it did not accept: re-prompt rather than hang.
		if sudoPrompt {
			return nil, errRemoteSudoAuth
		}
		return nil, errors.New("timed out waiting for the remote -Arch handshake")
	}
	if scanErr != nil {
		return nil, fmt.Errorf("reading remote output: %w", scanErr)
	}
	msg := "remote bootstrap failed (no -Arch line)"
	if sudoPrompt {
		msg += " (remote sudo authentication may have failed)"
	}
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
	if err := chownForSSH(tmpdir); err != nil {
		return 0, err
	}
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
			if err := sshSystem(sshCmd[0], "-S", ctl, "-O", "forward",
				"-L", lpath+":"+rpath, host); err != nil {
				return 0, err
			}
			if err := sshSystem(sshCmd[0], "-S", ctl, "-q", "-O", "stop", host); err != nil {
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
