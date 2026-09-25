package session

import (
	"fmt"
	"log"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32                               = syscall.NewLazyDLL("user32.dll")
	wtsapi32                             = syscall.NewLazyDLL("wtsapi32.dll")
	procRegisterClassExW                 = user32.NewProc("RegisterClassExW")
	procCreateWindowExW                  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW                   = user32.NewProc("DefWindowProcW")
	procDestroyWindow                    = user32.NewProc("DestroyWindow")
	procGetMessageW                      = user32.NewProc("GetMessageW")
	procTranslateMessage                 = user32.NewProc("TranslateMessage")
	procDispatchMessageW                 = user32.NewProc("DispatchMessageW")
	procPostMessageW                     = user32.NewProc("PostMessageW")
	procOpenInputDesktop                 = user32.NewProc("OpenInputDesktop")
	procCloseDesktop                     = user32.NewProc("CloseDesktop")
	procWTSRegisterSessionNotification   = wtsapi32.NewProc("WTSRegisterSessionNotification")
	procWTSUnRegisterSessionNotification = wtsapi32.NewProc("WTSUnRegisterSessionNotification")
)

const (
	WM_CLOSE                = 0x0010
	WM_WTSSESSION_CHANGE    = 0x02B1
	WTS_SESSION_LOCK        = 0x7
	WTS_SESSION_UNLOCK      = 0x8
	NOTIFY_FOR_THIS_SESSION = 0

	// ERROR_CLASS_ALREADY_EXISTS is the numeric Win32 error code (locale-independent,
	// unlike comparing err.Error() against the English string "Class already exists.").
	ERROR_CLASS_ALREADY_EXISTS = syscall.Errno(1410)
)

type WNDCLASSEXW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type MSG struct {
	Hwnd    uintptr
	Message uint32
	Wparam  uintptr
	Lparam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type Watcher struct {
	hwnd          uintptr
	onLockChanged func(locked bool)
}

func NewWatcher(onLockChanged func(locked bool)) *Watcher {
	return &Watcher{
		onLockChanged: onLockChanged,
	}
}

// IsWorkstationLocked checks if the workstation is currently locked or secure desktop is active.
func IsWorkstationLocked() bool {
	// DESKTOP_SWITCHDESKTOP = 0x0100
	hdesk, _, _ := procOpenInputDesktop.Call(0, 0, 0x0100)
	if hdesk == 0 {
		return true
	}
	procCloseDesktop.Call(hdesk)
	return false
}

// Start launches the Windows session notification watcher on a locked OS thread.
func (w *Watcher) Start() error {
	ready := make(chan error, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[Session] Recovered from session watcher panic: %v", r)
			}
		}()

		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		className, _ := syscall.UTF16PtrFromString("SignalLightLockWatcherClass")
		wndClass := WNDCLASSEXW{
			CbSize:        uint32(unsafe.Sizeof(WNDCLASSEXW{})),
			LpfnWndProc:   procDefWindowProcW.Addr(),
			LpszClassName: className,
		}

		atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wndClass)))
		if atom == 0 && err != syscall.Errno(0) && err != ERROR_CLASS_ALREADY_EXISTS {
			ready <- fmt.Errorf("failed to register window class: %w", err)
			return
		}

		wndName, _ := syscall.UTF16PtrFromString("SignalLightLockWatcher")
		hwndMessage := ^uintptr(2) // HWND_MESSAGE
		hwnd, _, err := procCreateWindowExW.Call(
			0,
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(wndName)),
			0,
			0, 0, 0, 0,
			hwndMessage,
			0, 0, 0,
		)
		if hwnd == 0 {
			ready <- fmt.Errorf("failed to create message window: %w", err)
			return
		}
		w.hwnd = hwnd

		r, _, err := procWTSRegisterSessionNotification.Call(hwnd, NOTIFY_FOR_THIS_SESSION)
		if r == 0 {
			procDestroyWindow.Call(hwnd)
			ready <- fmt.Errorf("failed to register session notification: %w", err)
			return
		}

		ready <- nil

		// Initial check
		if w.onLockChanged != nil {
			initLocked := IsWorkstationLocked()
			w.onLockChanged(initLocked)
		}

		var msg MSG
		for {
			res, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(res) <= 0 || msg.Message == WM_CLOSE {
				break
			}

			if msg.Message == WM_WTSSESSION_CHANGE {
				switch msg.Wparam {
				case WTS_SESSION_LOCK:
					// WTS_SESSION_LOCK also fires for UAC elevation prompts, Windows
					// Hello, and other secure-desktop switches — not just a real
					// Win+L lock — because they all use the same desktop-switch
					// mechanism under the hood. Confirm the real lock-screen host
					// process is actually running before treating this as "away";
					// otherwise a UAC prompt turns the light Yellow for a few
					// seconds until the matching (real) UNLOCK below corrects it.
					if isLogonUIRunning() {
						log.Println("[Session] Workstation locked (LogonUI confirmed)")
						if w.onLockChanged != nil {
							w.onLockChanged(true)
						}
					} else {
						log.Println("[Session] Ignored WTS_SESSION_LOCK: no LogonUI.exe process found (likely a UAC/Windows Hello secure-desktop prompt, not a real lock)")
					}
				case WTS_SESSION_UNLOCK:
					log.Println("[Session] Workstation unlocked")
					if w.onLockChanged != nil {
						w.onLockChanged(false)
					}
				}
			}

			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}

		procWTSUnRegisterSessionNotification.Call(hwnd)
		procDestroyWindow.Call(hwnd)
	}()

	return <-ready
}

// isLogonUIRunning reports whether Windows' real lock/logon screen host process
// (LogonUI.exe) is currently running. It retries briefly since LogonUI can take a
// moment to spawn after the WTS_SESSION_LOCK notification is delivered.
func isLogonUIRunning() bool {
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(150 * time.Millisecond)
		}
		found, ok := logonUIProcessExists()
		if !ok {
			// Couldn't enumerate processes at all: fail open to the WTS signal
			// rather than silently disabling lock detection.
			return true
		}
		if found {
			return true
		}
	}
	return false
}

func logonUIProcessExists() (found bool, ok bool) {
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, false
	}
	defer syscall.CloseHandle(snapshot)

	var entry syscall.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	for e := syscall.Process32First(snapshot, &entry); e == nil; e = syscall.Process32Next(snapshot, &entry) {
		name := syscall.UTF16ToString(entry.ExeFile[:])
		if strings.EqualFold(name, "LogonUI.exe") {
			return true, true
		}
	}
	return false, true
}

func (w *Watcher) Stop() {
	if w.hwnd != 0 {
		procPostMessageW.Call(w.hwnd, WM_CLOSE, 0, 0)
	}
}
