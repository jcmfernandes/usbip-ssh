package main

import (
	"io"
	"os"
	"reflect"
	"testing"
)

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	f()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestNsort(t *testing.T) {
	got := []string{"1-10", "1-2", "1-1.10", "1-1.2", "event10", "event2"}
	nsort(got)
	want := []string{"1-1.2", "1-1.10", "1-2", "1-10", "event2", "event10"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nsort = %v, want %v", got, want)
	}
}

func TestListDevices(t *testing.T) {
	withFixtureSysfs(t)
	out := captureStdout(t, func() {
		if err := listDevices(mustPattern("")); err != nil {
			t.Error(err)
		}
	})
	want := "1-1  2109:3431  001/002  0000 VIA USB2.0 Hub\n" +
		"  1-1.4  00da:8510  001/004  ABC Telink Wireless Receiver\n" +
		"      :1.0 030102 mouse   [usbhid] event4 mouse0\n" +
		"      :1.1 030101 kbd     [usbhid] event5\n"
	if out != want {
		t.Errorf("listDevices output:\n got %q\nwant %q", out, want)
	}
}

func TestListDevicesPattern(t *testing.T) {
	withFixtureSysfs(t)
	out := captureStdout(t, func() {
		if err := listDevices(mustPattern("Telink")); err != nil {
			t.Error(err)
		}
	})
	want := "  1-1.4  00da:8510  001/004  ABC Telink Wireless Receiver\n" +
		"      :1.0 030102 mouse   [usbhid] event4 mouse0\n" +
		"      :1.1 030101 kbd     [usbhid] event5\n"
	if out != want {
		t.Errorf("listDevices output:\n got %q\nwant %q", out, want)
	}
}
