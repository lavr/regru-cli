//go:build !darwin && !linux

package regruapi

import (
	"fmt"
	"runtime"
)

func bindToInterface(uintptr, string, string) error {
	return fmt.Errorf("binding to an interface is not supported on %s", runtime.GOOS)
}
