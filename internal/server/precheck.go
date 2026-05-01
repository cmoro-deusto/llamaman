package server

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

// PortAvailable reports whether host:port is free for binding right now.
// Used as a precheck before Spawn so the user gets a clear "port in use"
// signal instead of a confusing log line halfway through llama-server's
// startup.
//
// IPv6 hosts like "::1" should be passed bare; the function brackets them
// for net.Listen.
func PortAvailable(host string, port int) error {
	addr := joinHostPort(host, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("port %s in use: %w", addr, err)
	}
	// SO_REUSEADDR isn't enabled by default on Linux, so closing the
	// listener after a successful Listen returns the port to the
	// available pool immediately. No deadline races in practice.
	deadline := time.After(2 * time.Second)
	closed := make(chan struct{})
	go func() {
		_ = ln.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-deadline:
		return fmt.Errorf("port %s precheck timed out closing listener", addr)
	}
	return nil
}

func joinHostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
