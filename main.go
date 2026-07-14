package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

type attachOpts struct {
	vhub, noUnmount, noLinger, reverse bool
}

// remoteArgs is the JSON argument line sent to the remote payload.
type remoteArgs struct {
	Op        string `json:"op"` // "attach", "list" or "unbind"
	Pattern   string `json:"pattern"`
	Vhub      bool   `json:"vhub,omitempty"`
	NoUnmount bool   `json:"noUnmount,omitempty"`
	NoLinger  bool   `json:"noLinger,omitempty"`
	Verbose   bool   `json:"verbose,omitempty"`
}

func usage() {
	fmt.Fprintf(os.Stderr, `usage: %[1]s [global flags] COMMAND ...

  %[1]s attach HOST PATTERN     attach matching USB device from HOST
  %[1]s keep   HOST PATTERN     like attach, but reconnect forever with backoff
  %[1]s daemon HOST PATTERN     like keep, but detached from the tty, using syslog
  %[1]s list   HOST [PATTERN]   list USB devices on HOST
  %[1]s list --local [PATTERN]  list USB devices on this machine
  %[1]s detach BUSID...|all     detach locally attached usbip devices
  %[1]s unbind HOST PATTERN     release a device on HOST back to its normal driver

reverse mode (-r/--reverse) exports a local device to HOST instead; HOST is
the importer and PATTERN matches local devices:

  %[1]s attach -r HOST PATTERN  export a matching local USB device to HOST
  %[1]s keep   -r HOST PATTERN  like attach -r, but reconnect forever
  %[1]s daemon -r HOST PATTERN  like keep -r, but detached, using syslog
  %[1]s detach -r HOST BUSID...|all  tear down usbip devices on HOST
  %[1]s unbind -r PATTERN       release a local exported device (no ssh)

global flags (before the command):
  -v, --verbose        debug output
  --version            print version and exit
  --ssh 'ssh -p 2222'  ssh command to use (default "ssh")
  --sysfs PATH         sysfs mount point (default "/sys")
  --modprobe PATH      modprobe command (default "modprobe")

attach/keep/daemon flags (after the command):
  -r, --reverse        export a local device to HOST (roles reversed)
  --vhub               virtual hub mode: monitor for matching devices and
                       hot-attach them as they (re)appear; PATTERN may match
                       several
  --no-unmount         do not unmount filesystems backed by the device first
  --no-linger          the exporter side does not stay around to rebind the
                       device to its original driver when the connection drops

HOST is anything ssh accepts (root@10.0.0.7, an ssh alias, ...).
PATTERN is as printed by list: a busid like 3-3.1, a vid:pid like 03f0:e111,
or a regexp matched against the vid:pid, serial/manufacturer/product and
interface specs. A leading '!' negates the match.
`, progName)
	os.Exit(1)
}

func parseAttachArgs(name string, args []string) (string, string, attachOpts) {
	var o attachOpts
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = usage
	fs.BoolVar(&o.vhub, "vhub", false, "virtual hub mode")
	fs.BoolVar(&o.noUnmount, "no-unmount", false, "skip unmounting")
	fs.BoolVar(&o.noLinger, "no-linger", false, "no remote linger")
	fs.BoolVar(&o.reverse, "r", false, "reverse: export a local device to HOST")
	fs.BoolVar(&o.reverse, "reverse", false, "reverse: export a local device to HOST")
	fs.Parse(args)
	rest := fs.Args()
	if len(rest) != 2 {
		usage()
	}
	return rest[0], rest[1], o
}

// runSession runs one attach session, forward (import a HOST device) or
// reverse (export a local device to HOST).
func runSession(host, pattern string, o attachOpts) (int, error) {
	if o.reverse {
		return runReverseAttach(host, pattern, o)
	}
	return runAttach(host, pattern, o)
}

// parseReverse pulls a -r/--reverse flag off a detach/unbind arg list,
// returning whether it was set and the remaining positional arguments.
func parseReverse(name string, args []string) (bool, []string) {
	var reverse bool
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = usage
	fs.BoolVar(&reverse, "r", false, "reverse")
	fs.BoolVar(&reverse, "reverse", false, "reverse")
	fs.Parse(args)
	return reverse, fs.Args()
}

func main() {
	gfs := flag.NewFlagSet(progName, flag.ExitOnError)
	gfs.Usage = usage
	gfs.BoolVar(&verbose, "v", false, "verbose")
	gfs.BoolVar(&verbose, "verbose", false, "verbose")
	showVersion := gfs.Bool("version", false, "print version and exit")
	sshFlag := gfs.String("ssh", "ssh", "ssh command")
	gfs.StringVar(&sysfs, "sysfs", "/sys", "sysfs mount point")
	gfs.StringVar(&modprobe, "modprobe", "modprobe", "modprobe command")
	gfs.Parse(os.Args[1:])
	if *showVersion {
		fmt.Println(progName, version)
		os.Exit(0)
	}
	sshCmd = strings.Fields(*sshFlag)
	if len(sshCmd) == 0 {
		fatalf("empty --ssh command")
	}
	args := gfs.Args()
	if len(args) == 0 {
		usage()
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "attach", "keep", "daemon":
		host, pattern, o := parseAttachArgs(cmd, args)
		switch cmd {
		case "attach":
			code, err := runSession(host, pattern, o)
			if err != nil {
				fatalf("%s", err)
			}
			os.Exit(code)
		case "keep":
			keepCmd(host, pattern, o)
		case "daemon":
			daemonCmd(host, pattern, o)
		}
	case "list":
		lfs := flag.NewFlagSet("list", flag.ExitOnError)
		lfs.Usage = usage
		local := lfs.Bool("local", false, "list local devices")
		lfs.Parse(args)
		rest := lfs.Args()
		if *local {
			if len(rest) > 1 {
				usage()
			}
			if err := listDevices(mustPattern(strings.Join(rest, ""))); err != nil {
				fatalf("%s", err)
			}
		} else {
			if len(rest) < 1 || len(rest) > 2 {
				usage()
			}
			pattern := ""
			if len(rest) == 2 {
				pattern = rest[1]
			}
			os.Exit(remoteSimple(rest[0],
				remoteArgs{Op: "list", Pattern: pattern, Verbose: verbose}))
		}
	case "detach":
		reverse, rest := parseReverse(cmd, args)
		if reverse {
			// detach -r HOST BUSID...|all: tear down vhci ports on HOST.
			if len(rest) < 2 {
				usage()
			}
			os.Exit(remoteSimple(rest[0],
				remoteArgs{Op: "detach", Pattern: strings.Join(rest[1:], " "), Verbose: verbose}))
		}
		if len(rest) == 0 {
			usage()
		}
		if err := importerDetach(rest); err != nil {
			fatalf("%s", err)
		}
	case "unbind":
		reverse, rest := parseReverse(cmd, args)
		if reverse {
			// unbind -r PATTERN: release the local exporter device (no ssh).
			if len(rest) != 1 {
				usage()
			}
			os.Exit(reverseUnbind(rest[0]))
		}
		if len(rest) != 2 {
			usage()
		}
		os.Exit(remoteSimple(rest[0],
			remoteArgs{Op: "unbind", Pattern: rest[1], Verbose: verbose}))
	case "remote": // internal: payload side, args[0] is the JSON argument line
		if len(args) != 1 {
			usage()
		}
		remoteMain(args[0])
	case "linger": // internal: poll inherited sockets, rebind on hangup
		lingerMain(args)
	default:
		usage()
	}
}
