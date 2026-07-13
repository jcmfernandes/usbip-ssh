//go:build e2e

package e2e

import (
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// waitFor polls cond every 500ms until it returns true or timeout
// expires. On timeout the error names desc and the last error (or
// "condition false") from cond.
func waitFor(desc string, timeout time.Duration, cond func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	lastErr := fmt.Errorf("condition false")
	for {
		ok, err := cond()
		if ok {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %v waiting for %s: %v", timeout, desc, lastErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// Pinned to a specific immutable Debian cloud build so e2e runs are
// reproducible and verification needs no network. To refresh, bump imageBuild
// and imageSHA512 together (from that build's SHA512SUMS); old builds are
// eventually pruned from the mirrors, so the pinned URL will 404 someday.
const (
	imageBuild  = "20260712-2537"
	imageName   = "debian-13-generic-amd64-" + imageBuild + ".qcow2"
	imageBase   = "https://cloud.debian.org/images/cloud/trixie/" + imageBuild + "/"
	imageSHA512 = "78f658893d7aecb56288b86afebb72dcdb1a636e8e9db8bda64851a308697794678ceb5cd3b7c86afd5fb892afbc6baf9d2dbaceb7855347fde8660e8d68e667"
)

func fileSHA512(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func download(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return fmt.Errorf("GET %s: %w", url, err)
	}
	return f.Close()
}

// baseImage returns the path of the verified cached debian image,
// downloading it on first use. The image is pinned to a specific immutable
// build (see imageBuild) and re-verified against imageSHA512 on every run, so
// cached runs need no network.
func baseImage() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "usbip-ssh-e2e")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	img := filepath.Join(dir, imageName)
	if _, err := os.Stat(img); err != nil {
		log.Printf("e2e: downloading %s (~400 MB, one-time, cached in %s)", imageBase+imageName, dir)
		if err := download(imageBase+imageName, img+".part"); err != nil {
			return "", err
		}
		if err := os.Rename(img+".part", img); err != nil {
			return "", err
		}
	}
	got, err := fileSHA512(img)
	if err != nil {
		return "", err
	}
	if got != imageSHA512 {
		return "", fmt.Errorf("%s: checksum mismatch (delete it to re-download)", img)
	}
	return img, nil
}

const distBinary = "../dist/usbip-ssh_amd64"

type vm struct {
	dir     string
	sshPort int
	keyFile string
	qmp     *qmpClient
	qemu    *exec.Cmd
	seedOff func()
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

func checkPrereqs() error {
	for _, tool := range []string{"qemu-system-x86_64", "qemu-img", "ssh", "scp", "ssh-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("missing required tool %s", tool)
		}
	}
	kvm, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("KVM unavailable: %v", err)
	}
	kvm.Close()
	if _, err := os.Stat(distBinary); err != nil {
		return fmt.Errorf("%s not found - run these tests via 'make e2e'", distBinary)
	}
	return nil
}

// bootVM boots the shared test VM and returns once it is fully
// provisioned: reachable over ssh as root, cloud-init finished, QMP
// connected, and the usbip-ssh binary under test installed.
func bootVM(dir string) (*vm, error) {
	if err := checkPrereqs(); err != nil {
		return nil, err
	}
	base, err := baseImage()
	if err != nil {
		return nil, err
	}
	disk := filepath.Join(dir, "disk.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", "-b", base, "-F", "qcow2", disk).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("qemu-img create: %v: %s", err, out)
	}
	key := filepath.Join(dir, "id_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ssh-keygen: %v: %s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		return nil, err
	}
	seedPort, seedOff, err := seedServer(strings.TrimSpace(string(pub)))
	if err != nil {
		return nil, err
	}
	v := &vm{dir: dir, keyFile: key, seedOff: seedOff}
	if v.sshPort, err = freePort(); err != nil {
		seedOff()
		return nil, err
	}
	qmpSock := filepath.Join(dir, "qmp.sock")
	qemu := exec.Command("qemu-system-x86_64",
		"-M", "q35", "-enable-kvm", "-cpu", "host", "-smp", "2", "-m", "1024",
		"-display", "none",
		"-smbios", fmt.Sprintf("type=1,serial=ds=nocloud;s=http://10.0.2.2:%d/", seedPort),
		"-drive", "file="+disk+",if=virtio,format=qcow2",
		"-netdev", fmt.Sprintf("user,id=n0,hostfwd=tcp:127.0.0.1:%d-:22", v.sshPort),
		"-device", "virtio-net-pci,netdev=n0",
		"-device", "qemu-xhci,id=xhci",
		"-qmp", "unix:"+qmpSock+",server,wait=off",
		"-serial", "file:"+filepath.Join(dir, "console.log"),
	)
	qlog, err := os.Create(filepath.Join(dir, "qemu.log"))
	if err != nil {
		seedOff()
		return nil, err
	}
	qemu.Stdout, qemu.Stderr = qlog, qlog
	if err := qemu.Start(); err != nil {
		seedOff()
		return nil, fmt.Errorf("starting qemu: %v", err)
	}
	v.qemu = qemu
	fail := func(err error) (*vm, error) {
		v.teardown()
		return nil, err
	}
	log.Printf("e2e: booting VM (ssh port %d, run dir %s)", v.sshPort, dir)
	err = waitFor("ssh into the VM", 5*time.Minute, func() (bool, error) {
		out, err := v.ssh("true")
		if err != nil {
			return false, fmt.Errorf("%v: %s", err, out)
		}
		return true, nil
	})
	if err != nil {
		return fail(err)
	}
	// exit code 2 is "done, with recoverable errors" - fine for our purposes
	if out, err := v.ssh("cloud-init status --wait"); err != nil {
		var ee *exec.ExitError
		if !(errors.As(err, &ee) && ee.ExitCode() == 2) {
			return fail(fmt.Errorf("cloud-init: %v: %s", err, out))
		}
	}
	if v.qmp, err = qmpConnect(qmpSock); err != nil {
		return fail(err)
	}
	if out, err := v.scp(distBinary, "/usr/local/bin/usbip-ssh"); err != nil {
		return fail(fmt.Errorf("installing %s: %v: %s", distBinary, err, out))
	}
	return v, nil
}

func (v *vm) sshOpts() []string {
	return []string{
		"-i", v.keyFile,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=5",
	}
}

// ssh runs a shell command as root in the guest.
func (v *vm) ssh(cmd string) (string, error) {
	args := append(v.sshOpts(), "-p", strconv.Itoa(v.sshPort), "root@127.0.0.1", cmd)
	out, err := exec.Command("ssh", args...).CombinedOutput()
	return string(out), err
}

func (v *vm) scp(local, remote string) (string, error) {
	args := append(v.sshOpts(), "-P", strconv.Itoa(v.sshPort), local, "root@127.0.0.1:"+remote)
	out, err := exec.Command("scp", args...).CombinedOutput()
	return string(out), err
}

func (v *vm) teardown() {
	if v.qmp != nil {
		v.qmp.quit() // error ignored: qemu may die before replying
		v.qmp.close()
	}
	if v.qemu != nil {
		done := make(chan error, 1)
		go func() { done <- v.qemu.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			v.qemu.Process.Kill()
			<-done
		}
	}
	if v.seedOff != nil {
		v.seedOff()
	}
}

// startBg starts cmdline in the guest in its own process group and
// session (setsid), with output redirected to logPath in the guest.
// Returns the process group id for killBg.
func (v *vm) startBg(t *testing.T, cmdline, logPath string) string {
	t.Helper()
	out, err := v.ssh(fmt.Sprintf("setsid sh -c '%s >%s 2>&1 & echo $$'", cmdline, logPath))
	if err != nil {
		t.Fatalf("starting %q: %v: %s", cmdline, err, out)
	}
	pgid := strings.TrimSpace(out)
	if _, err := strconv.Atoi(pgid); err != nil {
		t.Fatalf("starting %q: expected a pid, got %q", cmdline, out)
	}
	return pgid
}

// killBg kills the process group started by startBg, taking usbip-ssh
// and its ssh children down together. Errors are ignored: the group
// may have already exited.
func (v *vm) killBg(pgid string) {
	v.ssh("kill -- -" + pgid)
}
