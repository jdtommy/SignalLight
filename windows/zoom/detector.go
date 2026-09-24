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
	user32                       = syscall.NewLazyDLL("user32.dll")
	procOpenDesktopW             = user32.NewProc("OpenDesktopW")
	procCloseDesktop             = user32.NewProc("CloseDesktop")
	procEnumDesktopWindows       = user32.NewProc("EnumDesktopWindows")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")

	defaultDesktop, _ = syscall.UTF16PtrFromString("Default")
	enumDesktopCb     = syscall.NewCallback(enumDesktopProc)
	enumMu            sync.Mutex
)

const (
	DESKTOP_READOBJECTS = 0x0001
	DESKTOP_ENUMERATE   = 0x0040
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

	lowerClass := strings.ToLower(className)
	lowerTitle := strings.ToLower(title)

	// 1. Zoom Presentation Content View Window (main in-meeting video window)
	if lowerClass == "zpcontentviewwnd" {
		ctx.meetingFound = true
		return 0 // Stop enum
	}

	// 2. Zoom Floating Video Window when multitasking / minimized during a meeting
	if lowerClass == "zpfloatvideowndclass" && (strings.Contains(lowerTitle, "zoom") || lowerTitle == "zfloatsizableparentwndcls") {
		ctx.meetingFound = true
		return 0
	}

	// 3. Window title explicitly indicates active meeting/webinar
	if strings.Contains(lowerTitle, "zoom meeting") || strings.Contains(lowerTitle, "zoom webinar") {
		ctx.meetingFound = true
		return 0
	}

	return 1
}

// MeetingDetector polls the system to detect if Zoom is in an active meeting.
type MeetingDetector struct {
	pollInterval time.Duration
	inMeeting    bool
	onChange     func(inMeeting bool)
	stopChan     chan struct{}
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
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[Zoom] Recovered from detector panic: %v. Restarting...", r)
				time.Sleep(2 * time.Second)
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
