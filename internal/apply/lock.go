package apply

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

type LockedError struct {
	Zone string
	Path string
}

func (e *LockedError) Error() string {
	return "zone_locked: zone " + e.Zone + " is locked by another regru process (" + e.Path + ")"
}

func (e *LockedError) Code() string { return "zone_locked" }

func LockZone(stateDir, userID, zoneName string) (func(), error) {
	if err := safeComponent("user id", userID); err != nil {
		return nil, err
	}
	if err := safeComponent("zone", zoneName); err != nil {
		return nil, err
	}
	dir, err := ensureDir(stateDir, "locks")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, userID+"-"+zoneName+".lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, &LockedError{Zone: zoneName, Path: path}
		}
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			f.Close()
		})
	}, nil
}
