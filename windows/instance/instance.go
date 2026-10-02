// Package instance keeps more than one copy of SignalLight from running in the same
// Windows session. Two copies would fight over the Bluetooth link and the dashboard
// port, and the installer needs a running copy to be the only one it has to close.
package instance

import (
	"syscall"
	"unsafe"
)

// MutexName is per-session ("Local\"), so other users signed in on the same PC
// can each run their own copy.
const MutexName = `Local\SignalLightSingleInstance`

const errorAlreadyExists = syscall.Errno(183) // ERROR_ALREADY_EXISTS

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutexW = kernel32.NewProc("CreateMutexW")
)

// Acquire claims the named mutex. It returns false if another instance already
// holds it. On success the handle is deliberately left open for the life of the
// process; Windows releases it when the process exits.
func Acquire(name string) (bool, error) {
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return false, err
	}
	h, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(p)))
	if h == 0 {
		return false, callErr
	}
	if callErr == errorAlreadyExists {
		_ = syscall.CloseHandle(syscall.Handle(h))
		return false, nil
	}
	return true, nil
}
