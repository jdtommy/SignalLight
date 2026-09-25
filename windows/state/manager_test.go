package state

import "testing"

func TestEvaluateAutoColorPriority(t *testing.T) {
	cases := []struct {
		name          string
		connected     bool
		zoomMeeting   bool
		sessionLocked bool
		zoomAway      bool
		want          LightColor
	}{
		{"disconnected beats everything else", false, true, true, true, ColorOff},
		{"zoom meeting beats lock", true, true, true, false, ColorRed},
		{"zoom meeting beats zoom-away", true, true, false, true, ColorRed},
		{"session locked, otherwise idle", true, false, true, false, ColorYellow},
		{"zoom presence away, otherwise idle", true, false, false, true, ColorYellow},
		{"connected and idle", true, false, false, false, ColorGreen},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &Manager{
				connected:     c.connected,
				zoomMeeting:   c.zoomMeeting,
				sessionLocked: c.sessionLocked,
				zoomAway:      c.zoomAway,
			}
			if got := m.evaluateAutoColor(); got != c.want {
				t.Errorf("evaluateAutoColor() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSetConnectedFalseForcesOff(t *testing.T) {
	m := NewManager()
	m.SetConnected(true)
	m.OnZoomMeetingChanged(true)
	if got := m.GetStatus().Color; got != ColorRed {
		t.Fatalf("expected RED while in a meeting, got %v", got)
	}

	m.SetConnected(false)
	if got := m.GetStatus().Color; got != ColorOff {
		t.Errorf("expected OFF when disconnected, got %v", got)
	}
}

func TestSetConnectedTrueRestoresAutoColor(t *testing.T) {
	m := NewManager()
	m.OnSessionLockChanged(true) // recorded even while disconnected
	m.SetConnected(true)
	if got := m.GetStatus().Color; got != ColorYellow {
		t.Errorf("expected YELLOW on reconnect with session already locked, got %v", got)
	}
}

func TestManualColorOverridesAutoUntilAutoModeRestored(t *testing.T) {
	m := NewManager()
	m.SetConnected(true)

	m.SetManualColor(ColorRed)
	st := m.GetStatus()
	if st.Color != ColorRed || st.Mode != ModeManual {
		t.Fatalf("expected manual RED, got color=%v mode=%v", st.Color, st.Mode)
	}

	// A Zoom meeting ending should NOT override a manual color.
	m.OnZoomMeetingChanged(false)
	if got := m.GetStatus().Color; got != ColorRed {
		t.Errorf("manual color should survive auto-signal changes, got %v", got)
	}

	// A screen lock/unlock should NOT override a manual color either.
	m.OnSessionLockChanged(true)
	if got := m.GetStatus().Color; got != ColorRed {
		t.Errorf("manual color should survive lock changes, got %v", got)
	}

	m.SetAutoMode()
	st = m.GetStatus()
	if st.Mode != ModeAuto || st.Color != ColorYellow {
		t.Errorf("expected AUTO mode to recompute to YELLOW (locked), got color=%v mode=%v", st.Color, st.Mode)
	}
}

func TestZoomPresenceAwayStaysInAutoMode(t *testing.T) {
	m := NewManager()
	m.SetConnected(true)

	m.OnZoomPresenceAway(true)
	st := m.GetStatus()
	if st.Color != ColorYellow || st.Mode != ModeAuto {
		t.Fatalf("expected AUTO/YELLOW after zoom-away, got color=%v mode=%v", st.Color, st.Mode)
	}

	// Unlike SetManualColor, presence-away must not get "stuck": once Zoom reports a
	// meeting starting, RED should still take over automatically.
	m.OnZoomMeetingChanged(true)
	if got := m.GetStatus().Color; got != ColorRed {
		t.Errorf("expected zoom meeting to still auto-trigger RED after a prior away signal, got %v", got)
	}
}
