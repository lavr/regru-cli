//go:build darwin || linux

package regruapi

import (
	"context"
	"net"
	"strings"
	"testing"
)

func loopbackName(t *testing.T) string {
	t.Helper()
	ifs, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 {
			return i.Name
		}
	}
	t.Skip("no loopback interface")
	return ""
}

func TestInterfaceBound(t *testing.T) {
	srv, n := newServer(t, body(`{"result":"success","answer":{}}`))
	c := testClient(srv.URL, &fakeClock{}, WithInterface(loopbackName(t)))
	if _, err := c.Call(context.Background(), Read, "nop", nil); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 1 {
		t.Fatalf("calls = %d", n.Load())
	}
}

func TestInterfaceUnknown(t *testing.T) {
	srv, n := newServer(t, body(`{"result":"success","answer":{}}`))
	c := testClient(srv.URL, &fakeClock{}, WithInterface("nosuchif0"), WithMaxAttempts(1))
	_, err := c.Call(context.Background(), Read, "nop", nil)
	if err == nil || !strings.Contains(err.Error(), "nosuchif0") {
		t.Fatalf("err = %v", err)
	}
	if n.Load() != 0 {
		t.Fatalf("calls = %d", n.Load())
	}
}
