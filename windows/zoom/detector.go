package zoom

import (
	"log"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32                         = syscall.NewLazyDLL("user32.dll")
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procOpenDesktopW               = user32.NewProc("OpenDesktopW")
	procCloseDesktop               = user32.NewProc("CloseDesktop")
	procEnumDesktopWindows         = user32.NewProc("EnumDesktopWindows")
	procGetWindowTextW             = user32.NewProc("GetWindowTextW")
	procGetClassNameW              = user32.NewProc("GetClassNameW")
	procIsWindowVisible            = user32.NewProc("IsWindowVisible")
	procGetWindowThreadProcessId   = user32.NewProc("GetWindowThreadProcessId")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")

	defaultDesktop, _ = syscall.UTF16PtrFromString("Default")
	enumDesktopCb     = syscall.NewCallback(enumDesktopProc)
	enumMu            sync.Mutex
)

const (
	DESKTOP_READOBJECTS                      = 0x0001
	DESKTOP_ENUMERATE                        = 0x0040
	PROCESS_QUERY_LIMITED_INFORMATION uint32 = 0x1000
)

type enumContext struct {
	meetingFound bool
}

// enumDesktopProc is allocated ONCE via syscall.NewCallback to avoid exhausting
// Go's internal Windows callback table (cbmax = 2000).
func enumDesktopProc(hwnd uintptr, lparam uintptr) uintptr {
	defer func() {
		_ = recover()
	}()

	if lparam == 0 {
		return 0
	}
	ctx := (*enumContext)(unsafe.Pointer(lparam))

	vis, _, _ := procIsWindowVisible.Call(hwnd)
	if vis == 0 {
		return 1 // Window not visible, skip
	}

	var bClass [256]uint16
	procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&bClass[0])), 256)
	className := syscall.UTF16ToString(bClass[:])

	var bTitle [256]uint16
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&bTitle[0])), 256)
	title := syscall.UTF16ToString(bTitle[:])

	// Heuristics 1 & 2: Zoom's own window classes are specific enough that no
	// process-identity check is needed.
	if isZoomVideoWindow(className, title) {
		ctx.meetingFound = true
		return 0 // Stop enum
	}

	// Heuristic 3: title-only match is weak (any app — a browser tab, a calendar
	// invite, a doc titled "How to join a Zoom meeting" — could coincidentally
	// contain this text), so also confirm the window actually belongs to Zoom's
	// own process before trusting it.
	if titleLooksLikeZoomMeeting(title) && processNameForWindow(hwnd) == "zoom.exe" {
		ctx.meetingFound = true
		return 0
	}

	return 1
}

// isZoomVideoWindow reports whether className/title match one of Zoom's own window
// classes for an active meeting (ZPContentViewWnd, ZPFloatVideoWndClass).
func isZoomVideoWindow(className, title string) bool {
	lowerClass := strings.ToLower(className)
	lowerTitle := strings.ToLower(title)

	if lowerClass == "zpcontentviewwnd" {
		return true
	}
	if lowerClass == "zpfloatvideowndclass" && (strings.Contains(lowerTitle, "zoom") || lowerTitle == "zfloatsizableparentwndcls") {
		return true
	}
	return false
}

// titleLooksLikeZoomMeeting reports whether a window title alone suggests an active
// Zoom meeting/webinar. Callers MUST additionally verify the window's owning process
// (see processNameForWindow) before treating this as a real match.
func titleLooksLikeZoomMeeting(title string) bool {
	lowerTitle := strings.ToLower(title)
	return strings.Contains(lowerTitle, "zoom meeting") || strings.Contains(lowerTitle, "zoom webinar")
}

// processNameForWindow returns the lowercase base executable name (e.g. "zoom.exe")
// of the process that owns hwnd, or "" if it can't be determined.
func processNameForWindow(hwnd uintptr) string {
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return ""
	}

	handle, err := syscall.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(handle)

	var buf [syscall.MAX_PATH]uint16
	size := uint32(len(buf))
	ret, _, _ := procQueryFullProcessImageNameW.Call(uintptr(handle), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if ret == 0 {
		return ""
	}

	fullPath := syscall.UTF16ToString(buf[:size])
	name := fullPath
	if idx := strings.LastIndexAny(fullPath, `\/`); idx >= 0 {
		name = fullPath[idx+1:]
	}
	return strings.ToLower(name)
}

// MeetingDetector polls the system to detect if Zoom is in an active meeting.
type MeetingDetector struct {
	pollInterval time.Duration
	inMeeting    bool
	onChange     func(inMeeting bool)
	stopChan     chan struct{}
	panicCount   int
}

// NewDetector creates a new Zoom meeting detector.
func NewDetector(interval time.Duration, onChange func(inMeeting bool)) *MeetingDetector {
	if interval < 500*time.Millisecond {
		interval = 1 * time.Second
	}
	return &MeetingDetector{
		pollInterval: interval,
		onChange:     onChange,
		stopChan:     make(chan struct{}),
	}
}

// Start begins polling for Zoom meeting status.
func (d *MeetingDetector) Start() {
	go func() {
		runStart := time.Now()
		defer func() {
			if r := recover(); r != nil {
				// Reset the streak after a sustained healthy run so one rare
				// panic doesn't count against a genuinely recurring crash-loop.
				if time.Since(runStart) > 30*time.Second {
					d.panicCount = 0
				}
				d.panicCount++
				backoff := time.Duration(d.panicCount) * 2 * time.Second
				if backoff > 60*time.Second {
					backoff = 60 * time.Second
				}
				log.Printf("[Zoom] Recovered from detector panic (#%d): %v. Restarting in %v...", d.panicCount, r, backoff)
				time.Sleep(backoff)
				d.Start()
			}
		}()

		// Run initial check immediately
		current := CheckMeetingActive()
		d.inMeeting = current
		if d.onChange != nil {
			d.onChange(current)
		}

		ticker := time.NewTicker(d.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				current := CheckMeetingActive()
				if current != d.inMeeting {
					d.inMeeting = current
					if d.onChange != nil {
						d.onChange(current)
					}
				}
			case <-d.stopChan:
				return
			}
		}
	}()
}

// Stop halts the detector polling.
func (d *MeetingDetector) Stop() {
	close(d.stopChan)
}

// IsMeetingActive returns current cached state.
func (d *MeetingDetector) IsMeetingActive() bool {
	return d.inMeeting
}

// CheckMeetingActive scans desktop windows to see if Zoom is actively in a meeting.
func CheckMeetingActive() bool {
	defer func() {
		_ = recover()
	}()

	enumMu.Lock()
	defer enumMu.Unlock()

	hdesk, _, _ := procOpenDesktopW.Call(uintptr(unsafe.Pointer(defaultDesktop)), 0, 0, DESKTOP_READOBJECTS|DESKTOP_ENUMERATE)
	if hdesk == 0 {
		return false
	}
	defer procCloseDesktop.Call(hdesk)

	var ctx enumContext
	procEnumDesktopWindows.Call(hdesk, enumDesktopCb, uintptr(unsafe.Pointer(&ctx)))
	return ctx.meetingFound
}
