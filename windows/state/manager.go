package state

import (
	"log"
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
	Color         LightColor `json:"color"`
	Mode          Mode       `json:"mode"`
	ZoomMeeting   bool       `json:"zoom_meeting"`
	SessionLocked bool       `json:"session_locked"`
	Connected     bool       `json:"connected"`
}

type Listener func(status Status)

type Manager struct {
	mu              sync.RWMutex
	color           LightColor
	mode            Mode
	lastManualColor LightColor
	zoomMeeting     bool
	zoomAway        bool
	sessionLocked   bool
	connected       bool
	listeners       []Listener
}

func NewManager() *Manager {
	return &Manager{
		color:           ColorOff, // Disconnected initially -> no light
		mode:            ModeAuto,
		lastManualColor: ColorGreen,
		zoomMeeting:     false,
		sessionLocked:   false,
		connected:       false,
	}
}

func (m *Manager) Subscribe(fn Listener) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listeners = append(m.listeners, fn)
}

func (m *Manager) notify() {
	status := Status{
		Color:         m.color,
		Mode:          m.mode,
		ZoomMeeting:   m.zoomMeeting,
		SessionLocked: m.sessionLocked,
		Connected:     m.connected,
	}
	for _, fn := range m.listeners {
		listener := fn
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[State] Recovered from listener panic: %v", r)
				}
			}()
			listener(status)
		}()
	}
}

func (m *Manager) GetStatus() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Status{
		Color:         m.color,
		Mode:          m.mode,
		ZoomMeeting:   m.zoomMeeting,
		SessionLocked: m.sessionLocked,
		Connected:     m.connected,
	}
}

// evaluateAutoColor calculates the appropriate color in AUTO mode.
// Priority: Disconnected (OFF) > Zoom Meeting (RED) > Screen Locked / Zoom Away (YELLOW) > Available (GREEN)
func (m *Manager) evaluateAutoColor() LightColor {
	if !m.connected {
		return ColorOff
	}
	if m.zoomMeeting {
		return ColorRed
	}
	if m.sessionLocked || m.zoomAway {
		return ColorYellow
	}
	return ColorGreen
}

func (m *Manager) SetConnected(connected bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connected != connected {
		m.connected = connected
		if !connected {
			m.color = ColorOff
		} else {
			if m.mode == ModeAuto {
				m.color = m.evaluateAutoColor()
			} else {
				if m.lastManualColor == "" {
					m.lastManualColor = ColorGreen
				}
				m.color = m.lastManualColor
			}
		}
		m.notify()
	}
}

// OnZoomMeetingChanged updates state when Zoom meeting begins or ends.
func (m *Manager) OnZoomMeetingChanged(inMeeting bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.zoomMeeting = inMeeting
	if m.mode == ModeAuto {
		m.color = m.evaluateAutoColor()
		m.notify()
	}
}

// OnSessionLockChanged updates state when Windows workstation is locked or unlocked.
func (m *Manager) OnSessionLockChanged(locked bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sessionLocked = locked
	if m.mode == ModeAuto {
		m.color = m.evaluateAutoColor()
		m.notify()
	}
}

// OnZoomPresenceAway updates state when Zoom's cloud presence reports the user as away.
// Unlike SetManualColor, this stays in AUTO mode so it doesn't permanently override
// future Zoom meeting / screen lock detection once presence changes back.
func (m *Manager) OnZoomPresenceAway(away bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.zoomAway = away
	if m.mode == ModeAuto {
		m.color = m.evaluateAutoColor()
		m.notify()
	}
}

// SetManualColor manually forces a color and sets mode to MANUAL.
func (m *Manager) SetManualColor(c LightColor) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastManualColor = c
	m.mode = ModeManual
	if m.connected {
		m.color = c
	} else {
		m.color = ColorOff
	}
	m.notify()
}

// SetAutoMode returns to automatic Zoom and Lock synchronization.
func (m *Manager) SetAutoMode() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.mode = ModeAuto
	m.color = m.evaluateAutoColor()
	m.notify()
}
