package hotkey

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procRegisterHotKey   = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey = user32.NewProc("UnregisterHotKey")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
)

const (
	MOD_ALT      = 0x0001
	MOD_CONTROL  = 0x0002
	MOD_SHIFT    = 0x0004
	MOD_WIN      = 0x0008
	MOD_NOREPEAT = 0x4000

	WM_HOTKEY = 0x0312

	// Hotkey IDs
	HOTKEY_RED    = 1
	HOTKEY_YELLOW = 2
	HOTKEY_GREEN  = 3
	HOTKEY_AUTO   = 4
)

type Action string

const (
	ActionRed    Action = "RED"
	ActionYellow Action = "YELLOW"
	ActionGreen  Action = "GREEN"
	ActionAuto   Action = "AUTO"
)

type MSG struct {
	Hwnd    uintptr
	Message uint32
	Wparam  uintptr
	Lparam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// Listener handles Windows global hotkeys.
type Listener struct {
	onAction func(action Action)
	stopChan chan struct{}
}

// NewListener initializes a new hotkey listener.
func NewListener(onAction func(action Action)) *Listener {
	return &Listener{
		onAction: onAction,
		stopChan: make(chan struct{}),
	}
}

// Start registers hotkeys and runs the Windows message pump on a locked OS thread.
func (l *Listener) Start() error {
	ready := make(chan error, 1)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		// Keycodes: 'R' = 0x52, 'Y' = 0x59, 'G' = 0x47, 'A' = 0x41
		hotkeys := []struct {
			id  uintptr
			mod uintptr
			vk  uintptr
			act Action
		}{
			{HOTKEY_RED, MOD_CONTROL | MOD_SHIFT | MOD_NOREPEAT, 0x52, ActionRed},
			{HOTKEY_YELLOW, MOD_CONTROL | MOD_SHIFT | MOD_NOREPEAT, 0x59, ActionYellow},
			{HOTKEY_GREEN, MOD_CONTROL | MOD_SHIFT | MOD_NOREPEAT, 0x47, ActionGreen},
			{HOTKEY_AUTO, MOD_CONTROL | MOD_SHIFT | MOD_NOREPEAT, 0x41, ActionAuto},
		}

		for _, hk := range hotkeys {
			r, _, err := procRegisterHotKey.Call(0, hk.id, hk.mod, hk.vk)
			if r == 0 {
				ready <- fmt.Errorf("failed to register hotkey ID %d: %v", hk.id, err)
				return
			}
		}

		ready <- nil

		defer func() {
			for _, hk := range hotkeys {
				procUnregisterHotKey.Call(0, hk.id)
			}
		}()

		var msg MSG
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(r) <= 0 {
				break
			}

			if msg.Message == WM_HOTKEY {
				switch msg.Wparam {
				case HOTKEY_RED:
					l.onAction(ActionRed)
				case HOTKEY_YELLOW:
					l.onAction(ActionYellow)
				case HOTKEY_GREEN:
					l.onAction(ActionGreen)
				case HOTKEY_AUTO:
					l.onAction(ActionAuto)
				}
			}
		}
	}()

	return <-ready
}
