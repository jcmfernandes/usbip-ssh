package main

import "testing"

func TestParseAttachArgs(t *testing.T) {
	host, pattern, o := parseAttachArgs("attach", []string{"--vhub", "--no-unmount", "root@h", "3-2"})
	if host != "root@h" || pattern != "3-2" || !o.vhub || !o.noUnmount || o.noLinger {
		t.Errorf("parseAttachArgs = %q %q %+v", host, pattern, o)
	}
	host, pattern, o = parseAttachArgs("keep", []string{"root@h", "Telink"})
	if host != "root@h" || pattern != "Telink" || o.vhub {
		t.Errorf("parseAttachArgs = %q %q %+v", host, pattern, o)
	}
}
