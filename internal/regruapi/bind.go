package regruapi

import (
	"context"
	"net"
	"net/http"
	"syscall"
	"time"
)

func boundTransport(iface string) *http.Transport {
	d := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(network, _ string, rc syscall.RawConn) error {
			var bindErr error
			if err := rc.Control(func(fd uintptr) { bindErr = bindToInterface(fd, network, iface) }); err != nil {
				return err
			}
			return bindErr
		},
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return d.DialContext(ctx, network, addr)
	}
	return t
}
