package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"signallight/firmware"
	"signallight/ota"
)

type idleLight struct{}

func (idleLight) IsConnected() bool                                  { return true }
func (idleLight) CanUpdateFirmware() bool                            { return true }
func (idleLight) FirmwareVersion() string                            { return "1.0.0" }
func (idleLight) UpdateFirmware([]byte, func(done, total int)) error { return nil }

func TestFirmwareEndpointsWithoutBluetooth(t *testing.T) {
	s := newTestServer("") // No BLE client, so no updater
	for _, path := range []string{"/api/firmware", "/api/firmware/check", "/api/firmware/install", "/api/firmware/upload"} {
		method := http.MethodPost
		if path == "/api/firmware" {
			method = http.MethodGet
		}
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "Bluetooth") {
			t.Errorf("%s %s: got %d %q, want 503 mentioning Bluetooth", method, path, w.Code, w.Body.String())
		}
	}
}

// These requests fail before the updater would check GitHub, so no network.
func TestFirmwareUploadRejectsBadFiles(t *testing.T) {
	s := newTestServer("")
	s.updater = firmware.NewUpdater(idleLight{})

	cases := map[string]struct {
		body []byte
		code int
		want string
	}{
		"not an app image": {[]byte("hello"), http.StatusConflict, "not an ESP32 app image"},
		"empty":            {nil, http.StatusConflict, "empty"},
		"too large":        {append([]byte{0xE9}, make([]byte, ota.MaxImageSize)...), http.StatusRequestEntityTooLarge, "too large"},
	}
	for name, c := range cases {
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/firmware/upload", bytes.NewReader(c.body)))
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s: got %d %q, want %d containing %q", name, w.Code, w.Body.String(), c.code, c.want)
		}
	}
}

func TestFirmwareUploadRejectsCrossOrigin(t *testing.T) {
	s := newTestServer("")
	s.updater = firmware.NewUpdater(idleLight{})
	req := httptest.NewRequest(http.MethodPost, "/api/firmware/upload", bytes.NewReader([]byte{0xE9, 0, 0}))
	req.Host = "localhost:9439"
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin upload: got %d, want 403", w.Code)
	}
}

func TestFirmwareRejectsGETForActions(t *testing.T) {
	s := newTestServer("")
	s.updater = firmware.NewUpdater(idleLight{})
	for _, path := range []string{"/api/firmware/check", "/api/firmware/install", "/api/firmware/upload"} {
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: got %d, want 405", path, w.Code)
		}
	}
}
