package state

import (
	"sync"
)

type LightColor string

const (
	ColorRed    LightColor = "RED"
	ColorYellow LightColor = "YELLOW"
	ColorGreen  LightColor = "GREEN"
	ColorOff    LightColor = "OFF"
)

type Mode string

const (
	ModeAuto   Mode = "AUTO"
	ModeManual Mode = "MANUAL"
)

type Status struct {
	Color       LightColor `json:"color"`
	Mode        Mode       `json:"mode"`
	ZoomMeeting bool       `json:"zoom_meeting"`
	Connected   bool       `json:"connected"`
}

type Listener func(status Status)

type Manager struct {
	mu          sync.RWMutex
	color       LightColor
	mode        Mode
	zoomMeeting bool
	connected   bool
	listeners   []Listener
}

func NewManager() *Manager {
	return &Manager{
		color:       ColorYellow, // Default to Yellow on startup
		mode:        ModeAuto,
		zoomMeeting: false,
		connected:   false,
	}
}

func (m *Manager) Subscribe(fn Listener) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listeners = append(m.listeners, fn)
}

func (m *Manager) notify() {
	status := Status{
		Color:       m.color,
		Mode:        m.mode,
		ZoomMeeting: m.zoomMeeting,
		Connected:   m.connected,
	}
	for _, fn := range m.listeners {
		go fn(status)
	}
}

func (m *Manager) GetStatus() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Status{
		Color:       m.color,
		Mode:        m.mode,
		ZoomMeeting: m.zoomMeeting,
		Connected:   m.connected,
	}
}

func (m *Manager) SetConnected(connected bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connected != connected {
		m.connected = connected
		m.notify()
	}
}

// OnZoomMeetingChanged updates state when Zoom meeting begins or ends.
func (m *Manager) OnZoomMeetingChanged(inMeeting bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.zoomMeeting = inMeeting
	if m.mode == ModeAuto {
		if inMeeting {
			m.color = ColorRed
		} else {
			m.color = ColorGreen // Zoom finished -> available
		}
		m.notify()
	}
}

// SetManualColor manually forces a color and sets mode to MANUAL.
func (m *Manager) SetManualColor(c LightColor) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.color = c
	m.mode = ModeManual
	m.notify()
}

// SetAutoMode returns to automatic Zoom synchronization.
func (m *Manager) SetAutoMode() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.mode = ModeAuto
	if m.zoomMeeting {
		m.color = ColorRed
	} else {
		m.color = ColorGreen
	}
	m.notify()
}
