package server

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestPortAvailableSuccess(t *testing.T) {
	// Pick a free port by listening on :0, then immediately close.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	if err := PortAvailable("127.0.0.1", port); err != nil {
		t.Fatalf("PortAvailable on free port: %v", err)
	}
}

func TestPortAvailableInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	err = PortAvailable("127.0.0.1", port)
	if err == nil {
		t.Fatal("expected error when port is bound")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(port)) {
		t.Errorf("error should mention port number; got %v", err)
	}
}
