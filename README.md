# usbip-ssh

> [!IMPORTANT]  
> This repository is a **hard fork** of https://github.com/turistu/usbip-ssh. I used an LLM to rewrite the original program in golang.

Attach USB devices from a remote Linux machine as if they were plugged
into the local one, using the kernel's USB/IP drivers and an ssh
connection. It does not use the USB/IP project's userland tools, opens no
TCP ports, and needs no software installed on the remote machine beyond an
ssh server: the binary ships itself over the ssh connection (linux/amd64
and linux/arm64 payloads are embedded).

The only remote configuration needed is ssh root access (copy your public
key to `~root/.ssh/authorized_keys`).

## Usage

Every invocation has the form:

```
usbip-ssh [global flags] COMMAND [command flags] ARGS...
```

The position matters: global flags (`-v`, `--ssh`, `--sudo`, ...) only work
**before** the command, while command flags (`-r`, `--vhub`, `--local`, ...)
only work **after** it. Flag parsing also stops at the first positional
argument, so command flags must come before `HOST`/`PATTERN` as well:

```
usbip-ssh -v --sudo attach -r root@pi Telink   # correct
usbip-ssh attach -r -v root@pi Telink          # wrong: -v is not an attach flag
usbip-ssh -r attach root@pi Telink             # wrong: -r is not a global flag
usbip-ssh attach root@pi -r Telink             # wrong: -r after HOST is a positional
```

The commands are:

```
usbip-ssh attach HOST PATTERN     attach matching USB device from HOST
usbip-ssh keep   HOST PATTERN     like attach, but reconnect forever with backoff
usbip-ssh daemon HOST PATTERN     like keep, but detached from the tty, using syslog
usbip-ssh list   HOST [PATTERN]   list USB devices on HOST
usbip-ssh list --local [PATTERN]  list USB devices on this machine
usbip-ssh detach BUSID...|all     detach locally attached usbip devices
usbip-ssh unbind HOST PATTERN     release a device on HOST back to its normal driver
```

The `-r`/`--reverse` flag runs any of these in reverse — exporting a
**local** device to HOST instead of importing one from it. In reverse mode
HOST is the importer and `PATTERN` matches local devices:

```
usbip-ssh attach -r HOST PATTERN         export a matching local device to HOST
usbip-ssh keep   -r HOST PATTERN         like attach -r, but reconnect forever
usbip-ssh daemon -r HOST PATTERN         like keep -r, but detached, using syslog
usbip-ssh detach -r HOST BUSID...|all    tear down usbip devices on HOST
usbip-ssh unbind -r PATTERN              release a local exported device (no ssh)
```

### Global flags

These go **before** the command, and are rejected after it:

- `-v`, `--verbose` — debug output.
- `--version` — print the version and exit.
- `--ssh 'ssh -p 2222 -J jump'` — the ssh command to use, like rsync's `-e`
  (default `ssh`).
- `--sysfs PATH` — sysfs mount point (default `/sys`).
- `--modprobe PATH` — modprobe command (default `modprobe`).
- `--sudo` — run the remote payload under `sudo -n`, so you can connect to
  HOST as a non-root user that has NOPASSWD sudo.
- `--sudo-prompt` — like `--sudo`, but prompts locally for the remote sudo
  password instead of requiring NOPASSWD. Cannot be combined with `--sudo`,
  and cannot be used with `daemon`, which detaches from the terminal and so
  has nowhere to prompt.
- `--ssh-user USER` — run the ssh client as USER, so that under `sudo` ssh
  uses your config, agent and known_hosts rather than root's. Needs to be
  run as root itself.

### Command flags

These go **after** the command but **before** its positional arguments.

`attach`, `keep` and `daemon`:

- `-r`, `--reverse` — export a local device to HOST (see above).
- `--vhub` — virtual hub mode: keep monitoring for matching devices and
  hot-attach them as they (re)appear. The pattern may then match several
  devices.
- `--no-unmount` — skip unmounting filesystems backed by the device before
  exporting it.
- `--no-linger` — make the exporter side not wait around to rebind the
  device to its original driver when the connection drops.

`detach` and `unbind`:

- `-r`, `--reverse` — reverse the roles (see above).

`list`:

- `--local` — list devices on this machine instead of on HOST. No HOST
  argument is taken.

### Patterns

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

Needs GNU Make 4.0+, bash, Go and upx — or run `mise install` to get the
Go and upx versions pinned by the included `mise.toml`:

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
