//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

// fakeQMP speaks just enough QMP: greeting, then one scripted reply per
// received command (events are injected before replies to test skipping).
// It records every received command line.
func fakeQMP(t *testing.T, replies []string) (path string, got *[]string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "qmp.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = &[]string{}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte(`{"QMP": {"version": {}, "capabilities": []}}` + "\n"))
		r := bufio.NewScanner(conn)
		for i := 0; r.Scan(); i++ {
			*got = append(*got, r.Text())
			if i < len(replies) {
				conn.Write([]byte(replies[i] + "\n"))
			}
		}
	}()
	return path, got
}

func TestQMPDeviceAdd(t *testing.T) {
	path, got := fakeQMP(t, []string{
		`{"return": {}}`, // qmp_capabilities
		`{"event": "NICE_WEATHER"}` + "\n" + `{"return": {}}`, // device_add, event first
	})
	c, err := qmpConnect(path)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.close()
	if err := c.deviceAdd("usb-kbd", "kbd0"); err != nil {
		t.Fatalf("deviceAdd: %v", err)
	}
	var req struct {
		Execute   string `json:"execute"`
		Arguments struct {
			Driver string `json:"driver"`
			ID     string `json:"id"`
			Bus    string `json:"bus"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal([]byte((*got)[1]), &req); err != nil {
		t.Fatalf("request not JSON: %v", err)
	}
	if req.Execute != "device_add" || req.Arguments.Driver != "usb-kbd" ||
		req.Arguments.ID != "kbd0" || req.Arguments.Bus != "xhci.0" {
		t.Fatalf("bad request: %s", (*got)[1])
	}
}

func TestQMPError(t *testing.T) {
	path, _ := fakeQMP(t, []string{
		`{"return": {}}`,
		`{"error": {"class": "GenericError", "desc": "no such device"}}`,
	})
	c, err := qmpConnect(path)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.close()
	err = c.deviceDel("nope")
	if err == nil || !strings.Contains(err.Error(), "no such device") {
		t.Fatalf("expected error mentioning desc, got: %v", err)
	}
}
