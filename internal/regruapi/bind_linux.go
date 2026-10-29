package regruapi

import (
	"fmt"
	"syscall"
)

func bindToInterface(fd uintptr, _, iface string) error {
	if err := syscall.BindToDevice(int(fd), iface); err != nil {
		return fmt.Errorf("interface %s: %w", iface, err)
	}
	return nil
}
