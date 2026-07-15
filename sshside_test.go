package main

import (
	"strings"
	"testing"
)

func withLogCapture(t *testing.T) *[]string {
	t.Helper()
	var got []string
	old := logf
	logf = func(pri int, msg string) { got = append(got, msg) }
	t.Cleanup(func() { logf = old })
	return &got
}

func TestCopyOutput(t *testing.T) {
	got := withLogCapture(t)
	copyOutput("ERROR: boom")
	copyOutput("WARNING: eek")
	copyOutput("chatter") // dropped: not verbose
	want := []string{"     boom\n", "     eek\n"}
	if strings.Join(*got, "|") != strings.Join(want, "|") {
		t.Errorf("copyOutput logged %q, want %q", *got, want)
	}
}

func TestRemoteBootstrap(t *testing.T) {
	oldSudo, oldPrompt := sudo, sudoPrompt
	t.Cleanup(func() { sudo, sudoPrompt = oldSudo, oldPrompt })

	sudo, sudoPrompt = false, false
	if got := remoteBootstrap(); got != bootstrap {
		t.Errorf("without --sudo: got %q, want the plain bootstrap", got)
	}

	sudo, sudoPrompt = true, false
	if got := remoteBootstrap(); got != "LC_ALL=C sudo -n -- "+bootstrap {
		t.Errorf("with --sudo: got %q, want sudo-wrapped bootstrap", got)
	}

	sudo, sudoPrompt = false, true
	if got := remoteBootstrap(); got != "LC_ALL=C sudo -S -k -p '' -- "+bootstrap {
		t.Errorf("with --sudo-prompt: got %q, want sudo -S wrapped bootstrap", got)
	}
}

func TestSudoFailRe(t *testing.T) {
	fails := []string{
		"Sorry, try again.",
		"sudo: 1 incorrect password attempt",
		"sudo: a password is required",
		"sudo: no password was provided",
		"pam_unix(sudo:auth): authentication failure; logname=...",
	}
	for _, s := range fails {
		if !sudoFailRe.MatchString(s) {
			t.Errorf("sudoFailRe should match sudo failure %q", s)
		}
	}
	ok := []string{
		"-Arch x86_64",
		"3-2 1050:0407 usbip-host /sys/devices/...",
		"Linux karma 6.12.0",
	}
	for _, s := range ok {
		if sudoFailRe.MatchString(s) {
			t.Errorf("sudoFailRe should not match ordinary line %q", s)
		}
	}
}

// TestBootstrapHandshake runs the real bootstrap line under /bin/sh (in
// place of ssh) with a tiny shell script as the "payload", verifying the
// -Arch handshake, the size/args/payload framing and the exec.
func TestBootstrapHandshake(t *testing.T) {
	oldSSH, oldPayload := sshCmd, payloadFor
	sshCmd = []string{"/bin/sh"}
	payloadFor = func(arch string) []byte {
		if arch == "" {
			t.Error("empty arch")
		}
		return []byte("#!/bin/sh\nprintf '%s\\n' \"-Fake $1 $2\"\n")
	}
	t.Cleanup(func() { sshCmd, payloadFor = oldSSH, oldPayload })

	s, err := startRemote(nil, "-c", remoteArgs{Op: "list", Pattern: "x y"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.pr.Close()
	var lines []string
	for s.out.Scan() {
		lines = append(lines, s.out.Text())
	}
	if code := waitExit(s.cmd); code != 0 {
		t.Errorf("exit = %d", code)
	}
	want := `-Fake remote {"op":"list","pattern":"x y"}`
	if len(lines) != 1 || lines[0] != want {
		t.Errorf("got %q, want [%q]", lines, want)
	}
}
