package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"signallight/state"
)

func TestGenerateSecretIsRandomHexOfExpectedLength(t *testing.T) {
	a, err := generateSecret()
	if err != nil {
		t.Fatalf("generateSecret() error: %v", err)
	}
	b, err := generateSecret()
	if err != nil {
		t.Fatalf("generateSecret() error: %v", err)
	}

	if len(a) != 32 {
		t.Errorf("expected a 32-character hex string (16 random bytes), got length %d: %q", len(a), a)
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Errorf("generateSecret() did not return valid hex: %v", err)
	}
	if a == b {
		t.Errorf("two calls to generateSecret() returned the same value — not random")
	}
}

func newTestServer(zoomSecret string) *Server {
	stateMgr := state.NewManager()
	stateMgr.SetConnected(true)
	return NewServer(":0", stateMgr, nil, zoomSecret)
}

func TestHandleSetRejectsGET(t *testing.T) {
	s := newTestServer("")
	req := httptest.NewRequest(http.MethodGet, "/api/set?color=red", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/set: got status %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleSetAllowsSameOriginPOST(t *testing.T) {
	s := newTestServer("")
	req := httptest.NewRequest(http.MethodPost, "/api/set?color=red", nil)
	req.Host = "localhost:9439"
	req.Header.Set("Origin", "http://localhost:9439")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("same-origin POST /api/set: got status %d, body %q", w.Code, w.Body.String())
	}
	if got := s.stateMgr.GetStatus().Color; got != state.ColorRed {
		t.Errorf("expected color RED after set, got %v", got)
	}
}

func TestHandleSetRejectsCrossOriginPOST(t *testing.T) {
	s := newTestServer("")
	req := httptest.NewRequest(http.MethodPost, "/api/set?color=red", nil)
	req.Host = "localhost:9439"
	req.Header.Set("Origin", "http://evil.example.com")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST /api/set: got status %d, want %d", w.Code, http.StatusForbidden)
	}
	if got := s.stateMgr.GetStatus().Color; got == state.ColorRed {
		t.Errorf("cross-origin request should not have been able to change color")
	}
}

func TestHandleSetAllowsNoOriginHeader(t *testing.T) {
	// Non-browser clients (curl, scripts) send no Origin/Referer at all and can't be
	// coerced into a request by a malicious webpage, so they must still be allowed.
	s := newTestServer("")
	req := httptest.NewRequest(http.MethodPost, "/api/set?color=green", nil)
	req.Host = "localhost:9439"
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST with no Origin header: got status %d, body %q", w.Code, w.Body.String())
	}
}

func TestHandleZoomWebhookRejectsUnsignedEvent(t *testing.T) {
	s := newTestServer("my-webhook-secret")
	body := `{"event":"meeting.started"}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/zoom", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("unsigned webhook event: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleZoomWebhookAcceptsValidSignature(t *testing.T) {
	secret := "my-webhook-secret"
	s := newTestServer(secret)
	body := `{"event":"meeting.started"}`
	timestamp := "1234567890"

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + timestamp + ":" + body))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/webhook/zoom", strings.NewReader(body))
	req.Header.Set("x-zm-signature", sig)
	req.Header.Set("x-zm-request-timestamp", timestamp)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("validly-signed webhook event: got status %d, body %q", w.Code, w.Body.String())
	}
	if got := s.stateMgr.GetStatus().Color; got != state.ColorRed {
		t.Errorf("expected RED after meeting.started, got %v", got)
	}
}

func TestHandleZoomWebhookUrlValidationSkipsSignatureCheck(t *testing.T) {
	secret := "my-webhook-secret"
	s := newTestServer(secret)
	body := `{"event":"endpoint.url_validation","payload":{"plainToken":"abc123"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/zoom", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("url_validation challenge: got status %d, body %q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "encryptedToken") {
		t.Errorf("expected encryptedToken in response body, got %q", w.Body.String())
	}
}
