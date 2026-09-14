package zoom

import (
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32                 = syscall.NewLazyDLL("user32.dll")
	procOpenDesktopW       = user32.NewProc("OpenDesktopW")
	procCloseDesktop       = user32.NewProc("CloseDesktop")
	procEnumDesktopWindows = user32.NewProc("EnumDesktopWindows")
	procGetWindowTextW     = user32.NewProc("GetWindowTextW")
	procGetClassNameW      = user32.NewProc("GetClassNameW")
	procIsWindowVisible    = user32.NewProc("IsWindowVisible")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
)

const (
	DESKTOP_READOBJECTS = 0x0001
	DESKTOP_ENUMERATE   = 0x0040
)

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
	ticker := time.NewTicker(d.pollInterval)
	go func() {
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
				ticker.Stop()
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
	dName, _ := syscall.UTF16PtrFromString("Default")
	hdesk, _, _ := procOpenDesktopW.Call(uintptr(unsafe.Pointer(dName)), 0, 0, DESKTOP_READOBJECTS|DESKTOP_ENUMERATE)
	if hdesk == 0 {
		return false
	}
	defer procCloseDesktop.Call(hdesk)

	meetingFound := false

	cb := syscall.NewCallback(func(hwnd uintptr, lparam uintptr) uintptr {
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		if vis == 0 {
			return 1 // Window not visible, skip
		}

		bClass := make([]uint16, 256)
		procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&bClass[0])), 256)
		className := syscall.UTF16ToString(bClass)

		bTitle := make([]uint16, 256)
		procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&bTitle[0])), 256)
		title := syscall.UTF16ToString(bTitle)

		lowerClass := strings.ToLower(className)
		lowerTitle := strings.ToLower(title)

		// 1. Zoom Presentation Content View Window (main in-meeting video window)
		if lowerClass == "zpcontentviewwnd" {
			meetingFound = true
			return 0 // Stop enum
		}

		// 2. Zoom Floating Video Window when multitasking / minimized during a meeting
		if lowerClass == "zpfloatvideowndclass" && (strings.Contains(lowerTitle, "zoom") || lowerTitle == "zfloatsizableparentwndcls") {
			meetingFound = true
			return 0
		}

		// 3. Window title explicitly indicates active meeting/webinar
		if strings.Contains(lowerTitle, "zoom meeting") || strings.Contains(lowerTitle, "zoom webinar") {
			meetingFound = true
			return 0
		}

		return 1
	})

	procEnumDesktopWindows.Call(hdesk, cb, 0)
	return meetingFound
}
