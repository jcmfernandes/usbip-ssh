//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"net/http"
)

const metaData = "instance-id: usbip-ssh-e2e\nlocal-hostname: e2e\n"

// userData is the cloud-config for one VM: root ssh access for the shared
// harness key, the same key installed as root's own identity (so the guest
// can ssh to its peer non-interactively), accept-new host keys, and a static
// IP on the inter-VM NIC selected by its MAC.
func userData(pubkey, privB64, linkMAC, linkIP string) string {
	return fmt.Sprintf(`#cloud-config
disable_root: false
write_files:
  - path: /root/.ssh/id_ed25519
    permissions: '0600'
    encoding: b64
    content: %s
runcmd:
  - mkdir -p /root/.ssh
  - chmod 700 /root/.ssh
  - echo '%s' >> /root/.ssh/authorized_keys
  - chmod 600 /root/.ssh/authorized_keys
  - printf 'Host *\n  StrictHostKeyChecking accept-new\n' > /root/.ssh/config
  - chmod 600 /root/.ssh/config
  - for n in /sys/class/net/*; do if [ "$(cat $n/address)" = "%s" ]; then ip addr add %s/24 dev "$(basename $n)"; ip link set "$(basename $n)" up; fi; done
`, privB64, pubkey, linkMAC, linkIP)
}

// seedServer serves the NoCloud seed on 127.0.0.1; the guest reaches it as
// http://10.0.2.2:<port>/ through qemu user-mode networking.
func seedServer(pubkey, privB64, linkMAC, linkIP string) (int, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/meta-data", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, metaData)
	})
	mux.HandleFunc("/user-data", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, userData(pubkey, privB64, linkMAC, linkIP))
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	return ln.Addr().(*net.TCPAddr).Port, func() { srv.Close() }, nil
}
