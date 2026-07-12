# usbip-ssh

Attach USB devices from a remote Linux machine as if they were plugged
into the local one, using the kernel's USB/IP drivers and an ssh
connection. It does not use the USB/IP project's userland tools, opens no
TCP ports, and needs no software installed on the remote machine beyond an
ssh server: the binary ships itself over the ssh connection (linux/amd64
and linux/arm64 payloads are embedded).

The only remote configuration needed is ssh root access (copy your public
key to `~root/.ssh/authorized_keys`).

## Usage

```
usbip-ssh attach HOST PATTERN     attach matching USB device from HOST
usbip-ssh keep   HOST PATTERN     like attach, but reconnect forever with backoff
usbip-ssh daemon HOST PATTERN     like keep, but detached from the tty, using syslog
usbip-ssh list   HOST [PATTERN]   list USB devices on HOST
usbip-ssh list --local [PATTERN]  list USB devices on this machine
usbip-ssh detach BUSID...|all     detach locally attached usbip devices
usbip-ssh unbind HOST PATTERN     release a device on HOST back to its normal driver
```

Global flags (before the command): `-v`/`--verbose`; `--ssh 'ssh -p 2222 -J
jump'` to choose the ssh command (like rsync's `-e`); `--sysfs PATH`;
`--modprobe PATH`.

Attach flags (after the command): `--vhub` keeps monitoring HOST and
hot-attaches matching devices as they (re)appear — the pattern may then
match several devices; `--no-unmount` skips unmounting filesystems backed
by the device before exporting it; `--no-linger` makes the remote side not
wait around to rebind the device to its original driver when the
connection drops.

`PATTERN` is as printed by `list`: a busid like `3-3.1`, a `vid:pid` like
`03f0:e111` (leading zeros optional), or a regexp matched against the
vid:pid, serial/manufacturer/product strings and the interface
class/subclass/protocol hex specs. A leading `!` negates the match.

## Example

```
# usbip-ssh list root@raspberry-pi
1-1  2109:3431  001/002  USB2.0 Hub
  1-1.4  00da:8510  001/004  Telink Wireless Receiver
      :1.0 030102 mouse   [usbhid] event4 event5 event6 hidraw0 mouse0
      :1.1 030101 kbd     [usbhid] event7 hidraw1
# usbip-ssh -v attach root@raspberry-pi Telink
...
```

After which the keyboard/mouse connected to the remote `raspberry-pi` can
be used as if it were connected to the local machine.

To set it up permanently (e.g. from an `/etc/boot.d` script on Debian):

```
exec /usr/local/bin/usbip-ssh daemon root@raspberry-pi Telink
```

`daemon` logs through syslog(3) instead of stderr and keeps retrying (when
the remote machine is unreachable, the device is unplugged, the connection
breaks, ...), spacing out retries depending on how long the previous
attempt held up.

## Building

Needs Go and make:

```
make            # dist/usbip-ssh_amd64 and dist/usbip-ssh_arm64
sudo make install
```

Each `dist/` binary embeds payload builds for both architectures, so
either one can import from amd64 and arm64 remotes alike. Plain `go
build` only works with `-tags payload` (a payload build cannot ship
itself); run `make payloads` once if you want your editor/gopls to be
happy with the default build tags, and run tests with `go test -tags
payload ./...` or `make test`.

## How does USB/IP work

Both the `usbip_host` driver (on the exporting/remote machine) and
`vhci_hcd` (on the local/importing machine) work by tunneling the USB
protocol over a socket file descriptor passed in by a userland process;
despite the "IP" in "USB/IP", the socket can be *any* kind of stream
socket, including a unix domain socket.

## Why this program has to suck so much then

Despite it being theoretically easy to use any reliable transport for
USB/IP (not just open TCP connections on a "secure" lan), limitations in
ssh make everything much harder than it has to be: ssh is not able to
forward simple file descriptors, nor use a unix domain socket for the
stdin/out of the remote program (the only options being a pair of pipes or
a pseudo-terminal).

The only way to access ssh's "channel" abstraction is by setting up a TCP
or unix socket forwarding, and having to use that turns everything into a
mess of master and slave ssh commands, temporary directories and socket
files which have to be cleaned up, and extra processes which connect and
listen to them and race against each other.
