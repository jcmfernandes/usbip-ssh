//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"net/http"
)

const metaData = "instance-id: usbip-ssh-e2e\nlocal-hostname: e2e\n"

// userData is the cloud-config for the VM: root ssh access for the
// harness key, and a self-trusted root keypair so that usbip-ssh's
// "ssh root@localhost" inside the VM works non-interactively.
func userData(pubkey string) string {
	return fmt.Sprintf(`#cloud-config
disable_root: false
runcmd:
  - mkdir -p /root/.ssh
  - chmod 700 /root/.ssh
  - echo '%s' >> /root/.ssh/authorized_keys
  - ssh-keygen -t ed25519 -N '' -f /root/.ssh/id_ed25519
  - cat /root/.ssh/id_ed25519.pub >> /root/.ssh/authorized_keys
  - chmod 600 /root/.ssh/authorized_keys
  - printf 'Host localhost\n  StrictHostKeyChecking accept-new\n' > /root/.ssh/config
`, pubkey)
}

// seedServer serves the NoCloud seed on 127.0.0.1; the guest reaches it
// as http://10.0.2.2:<port>/ through qemu user-mode networking.
func seedServer(pubkey string) (int, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/meta-data", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, metaData)
	})
	mux.HandleFunc("/user-data", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, userData(pubkey))
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	return ln.Addr().(*net.TCPAddr).Port, func() { srv.Close() }, nil
}
