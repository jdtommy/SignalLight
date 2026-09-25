package zoom

import (
	"testing"
)

func TestCheckMeetingActiveMultipleCalls(t *testing.T) {
	// Call CheckMeetingActive multiple times to verify no panic or callback accumulation
	for i := 0; i < 2500; i++ {
		_ = CheckMeetingActive()
	}
}

func TestIsZoomVideoWindow(t *testing.T) {
	cases := []struct {
		name      string
		className string
		title     string
		want      bool
	}{
		{"content view window, any title", "ZPContentViewWnd", "anything", true},
		{"content view window, case-insensitive class", "zpcontentviewwnd", "anything", true},
		{"float video window with zoom in title", "ZPFloatVideoWndClass", "Zoom Meeting", true},
		{"float video window, exact sizable title", "ZPFloatVideoWndClass", "zFloatSizableParentWndCls", true},
		{"float video window, unrelated title", "ZPFloatVideoWndClass", "Random Video", false},
		{"unrelated class and title", "Notepad", "untitled.txt", false},
		{"empty class and title", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isZoomVideoWindow(c.className, c.title); got != c.want {
				t.Errorf("isZoomVideoWindow(%q, %q) = %v, want %v", c.className, c.title, got, c.want)
			}
		})
	}
}

func TestTitleLooksLikeZoomMeeting(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  bool
	}{
		{"exact meeting title", "Zoom Meeting", true},
		{"exact webinar title", "Zoom Webinar", true},
		{"case-insensitive", "ZOOM MEETING - Q3 Planning", true},
		{"false positive: browser tab about zoom", "How to schedule a Zoom Meeting - Google Docs", true},
		{"unrelated title", "Untitled - Notepad", false},
		{"empty title", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := titleLooksLikeZoomMeeting(c.title); got != c.want {
				t.Errorf("titleLooksLikeZoomMeeting(%q) = %v, want %v", c.title, got, c.want)
			}
		})
	}
}
