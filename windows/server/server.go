package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"signallight/ble"
	"signallight/config"
	"signallight/state"
)

type Server struct {
	addr       string
	stateMgr   *state.Manager
	bleClient  *ble.Client
	zoomSecret string // Optional Zoom webhook secret token
	mux        *http.ServeMux
	httpServer *http.Server
}

func NewServer(addr string, stateMgr *state.Manager, bleClient *ble.Client, zoomSecret string) *Server {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = ":8080"
	} else if !strings.Contains(addr, ":") {
		addr = ":" + addr
	}
	s := &Server{
		addr:       addr,
		stateMgr:   stateMgr,
		bleClient:  bleClient,
		zoomSecret: zoomSecret,
		mux:        http.NewServeMux(),
	}
	s.routes()
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           recoveryMiddleware(s.mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s
}

// jsonError writes an error as valid JSON. Unlike hand-concatenating `{"error":"`+err.Error()+`"}`,
// this can't produce invalid JSON when the underlying error message contains a quote,
// backslash, or newline (plausible from BLE/library errors).
func jsonError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[Web] Recovered from HTTP handler panic: %v", rec)
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleDashboard)
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/api/set", sameOriginOnly(s.handleSet))
	s.mux.HandleFunc("/api/ble/config", s.handleBLEConfig)
	s.mux.HandleFunc("/api/ble/scan", sameOriginOnly(s.handleBLEScan))
	s.mux.HandleFunc("/api/ble/pair", sameOriginOnly(s.handleBLEPair))
	s.mux.HandleFunc("/api/ble/unpair", sameOriginOnly(s.handleBLEUnpair))
	s.mux.HandleFunc("/webhook/zoom", s.handleZoomWebhook)
}

// sameOriginOnly blocks cross-site browser requests (CSRF) to state-changing endpoints.
// The dashboard is unauthenticated by design (trusted single-user localhost tool), so any
// page a user's browser has open could otherwise silently POST/GET these endpoints and
// change the light's color or unpair the hardware. A browser always sets Origin (or, on
// older browsers, Referer) on cross-origin fetch/form requests; we require it to match
// this server's own Host when present. Non-browser clients (curl, scripts) send neither
// header and are allowed through, since they can't be coerced into a request by a webpage.
func sameOriginOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = r.Header.Get("Referer")
		}
		if origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, r.Host) {
				http.Error(w, `{"error":"cross-origin request rejected"}`, http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) Start(listeners ...net.Listener) error {
	var listener net.Listener
	if len(listeners) > 0 && listeners[0] != nil {
		listener = listeners[0]
		s.addr = listener.Addr().String()
	}

	displayAddr := s.addr
	if strings.HasPrefix(displayAddr, ":") {
		displayAddr = "localhost" + displayAddr
	}
	log.Printf("[Web] SignalLight web interface listening at http://%s", displayAddr)
	go func() {
		var err error
		if listener != nil {
			err = s.httpServer.Serve(listener)
		} else {
			err = s.httpServer.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Printf("[Web] Server error: %v", err)
		}
	}()
	return nil
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	status := s.stateMgr.GetStatus()
	_ = json.NewEncoder(w).Encode(status)
}

func (s *Server) handleSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	colorParam := strings.ToUpper(r.URL.Query().Get("color"))
	if colorParam == "" {
		_ = r.ParseForm()
		colorParam = strings.ToUpper(r.FormValue("color"))
	}

	switch colorParam {
	case "RED":
		s.stateMgr.SetManualColor(state.ColorRed)
	case "YELLOW":
		s.stateMgr.SetManualColor(state.ColorYellow)
	case "GREEN":
		s.stateMgr.SetManualColor(state.ColorGreen)
	case "OFF":
		s.stateMgr.SetManualColor(state.ColorOff)
	case "AUTO":
		s.stateMgr.SetAutoMode()
	default:
		http.Error(w, "Invalid color. Use red, yellow, green, off, or auto.", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.stateMgr.GetStatus())
}

func (s *Server) handleBLEConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	cfg := config.GetCached()
	masked := ""
	if len(cfg.SharedSecret) > 0 {
		masked = "********"
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"target_mac":    cfg.TargetMAC,
		"device_name":   cfg.DeviceName,
		"paired":        cfg.Paired,
		"shared_secret": masked,
		"config_path":   config.GetConfigPath(),
	})
}

func (s *Server) handleBLEScan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.bleClient == nil {
		http.Error(w, `{"error":"BLE client not initialized"}`, http.StatusBadRequest)
		return
	}

	devices, err := s.bleClient.ScanNearbyDevices(6 * time.Second)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err)
		return
	}

	_ = json.NewEncoder(w).Encode(devices)
}

func (s *Server) handleBLEPair(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Address string `json:"address"`
		Name    string `json:"name"`
		Secret  string `json:"secret"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request format"}`, http.StatusBadRequest)
		return
	}

	req.Address = strings.ToUpper(strings.TrimSpace(req.Address))
	req.Name = strings.TrimSpace(req.Name)
	req.Secret = strings.TrimSpace(req.Secret)

	if req.Address == "" || req.Name == "" || req.Secret == "" {
		http.Error(w, `{"error":"address, name, and secret are required"}`, http.StatusBadRequest)
		return
	}

	if s.bleClient == nil {
		http.Error(w, `{"error":"BLE client not initialized"}`, http.StatusInternalServerError)
		return
	}

	if err := s.bleClient.PairDevice(req.Address, req.Name, req.Secret); err != nil {
		jsonError(w, http.StatusInternalServerError, err)
		return
	}

	// Save to config
	cached := config.GetCached()
	cfg := &config.Config{
		TargetMAC:    req.Address,
		DeviceName:   req.Name,
		SharedSecret: req.Secret,
		Paired:       true,
		WebPort:      cached.WebPort,
	}
	if err := config.Save(cfg); err != nil {
		log.Printf("[Web] Warning: failed to write config file: %v", err)
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"device":  cfg,
	})
}

func (s *Server) handleBLEUnpair(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.bleClient != nil {
		_ = s.bleClient.UnpairCurrent()
	}

	_ = config.Clear()

	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// verifyZoomSignature validates Zoom's per-request HMAC (x-zm-signature /
// x-zm-request-timestamp headers) per Zoom's webhook signing spec:
// signature = "v0=" + hex(HMAC-SHA256("v0:{timestamp}:{body}", secretToken)).
// This is separate from the one-time endpoint.url_validation challenge below, and is
// what actually stops a stranger who finds the webhook URL from forging meeting/presence
// events to remotely change the light.
func (s *Server) verifyZoomSignature(r *http.Request, body []byte) bool {
	if s.zoomSecret == "" {
		return false
	}
	sigHeader := r.Header.Get("x-zm-signature")
	tsHeader := r.Header.Get("x-zm-request-timestamp")
	if sigHeader == "" || tsHeader == "" {
		return false
	}

	message := "v0:" + tsHeader + ":" + string(body)
	h := hmac.New(sha256.New, []byte(s.zoomSecret))
	h.Write([]byte(message))
	expected := "v0=" + hex.EncodeToString(h.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(sigHeader))
}

// handleZoomWebhook handles Zoom Marketplace webhook events and URL validation challenge.
func (s *Server) handleZoomWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var payload struct {
		Event   string `json:"event"`
		Payload struct {
			PlainToken string `json:"plainToken"`
			Object     struct {
				PresenceStatus string `json:"presence_status"`
			} `json:"object"`
		} `json:"payload"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Zoom Webhook URL Validation challenge: this is the one request that legitimately
	// arrives before any signature can be verified (it's how Zoom proves you control the
	// secret in the first place), so it's intentionally exempt from the check below.
	if payload.Event == "endpoint.url_validation" {
		plainToken := payload.Payload.PlainToken
		h := hmac.New(sha256.New, []byte(s.zoomSecret))
		h.Write([]byte(plainToken))
		encryptedToken := hex.EncodeToString(h.Sum(nil))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"plainToken":     plainToken,
			"encryptedToken": encryptedToken,
		})
		log.Printf("[Zoom Webhook] Successfully responded to validation challenge.")
		return
	}

	if !s.verifyZoomSignature(r, body) {
		log.Printf("[Zoom Webhook] Rejected event with missing/invalid signature: %s", payload.Event)
		http.Error(w, `{"error":"invalid signature"}`, http.StatusUnauthorized)
		return
	}

	log.Printf("[Zoom Webhook] Received event: %s", payload.Event)

	switch payload.Event {
	case "meeting.started", "meeting.participant_joined":
		s.stateMgr.OnZoomMeetingChanged(true)
	case "meeting.ended", "meeting.participant_left":
		s.stateMgr.OnZoomMeetingChanged(false)
	case "user.presence_status_updated":
		status := strings.ToLower(payload.Payload.Object.PresenceStatus)
		switch status {
		case "in_meeting", "do_not_disturb":
			s.stateMgr.OnZoomMeetingChanged(true)
		case "available":
			s.stateMgr.OnZoomMeetingChanged(false)
			s.stateMgr.OnZoomPresenceAway(false)
		case "away":
			// Stays in AUTO mode (unlike SetManualColor) so meeting/lock detection
			// keeps working once presence changes again.
			s.stateMgr.OnZoomMeetingChanged(false)
			s.stateMgr.OnZoomPresenceAway(true)
		}
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.New("dashboard").Parse(dashboardHTML)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = tmpl.Execute(w, nil)
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>SignalLight Controller</title>
    <style>
        :root {
            --bg: #0f172a;
            --card-bg: #1e293b;
            --card-sub: #0f172a80;
            --text: #f8fafc;
            --text-dim: #94a3b8;
            --red: #ef4444;
            --yellow: #eab308;
            --green: #22c55e;
            --blue: #3b82f6;
            --border: rgba(255, 255, 255, 0.08);
        }
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            background: var(--bg);
            color: var(--text);
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            padding: 24px 16px;
        }
        .container {
            background: var(--card-bg);
            border-radius: 20px;
            padding: 32px 28px;
            width: 100%;
            max-width: 500px;
            box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.5);
            border: 1px solid var(--border);
            text-align: center;
        }
        h1 { font-size: 1.6rem; margin-bottom: 4px; font-weight: 700; letter-spacing: -0.5px; }
        .subtitle { color: var(--text-dim); font-size: 0.85rem; margin-bottom: 22px; }
        
        .traffic-light {
            display: flex;
            flex-direction: column;
            gap: 14px;
            background: #090d16;
            padding: 18px;
            border-radius: 50px;
            width: 96px;
            margin: 0 auto 22px;
            border: 3px solid #334155;
            box-shadow: inset 0 2px 10px rgba(0,0,0,0.8);
        }
        .bulb {
            width: 60px;
            height: 60px;
            border-radius: 50%;
            background: #1e293b;
            transition: all 0.3s ease;
            box-shadow: inset 0 3px 6px rgba(0,0,0,0.6);
        }
        .bulb.red.active { background: var(--red); box-shadow: 0 0 25px var(--red), inset 0 0 10px rgba(255,255,255,0.4); }
        .bulb.yellow.active { background: var(--yellow); box-shadow: 0 0 25px var(--yellow), inset 0 0 10px rgba(255,255,255,0.4); }
        .bulb.green.active { background: var(--green); box-shadow: 0 0 25px var(--green), inset 0 0 10px rgba(255,255,255,0.4); }

        .status-badge {
            display: inline-block;
            font-size: 1rem;
            font-weight: 700;
            padding: 7px 18px;
            border-radius: 9999px;
            margin-bottom: 16px;
            text-transform: uppercase;
            letter-spacing: 0.5px;
        }
        .status-badge.RED { background: rgba(239, 68, 68, 0.2); color: var(--red); border: 1px solid var(--red); }
        .status-badge.YELLOW { background: rgba(234, 179, 8, 0.2); color: var(--yellow); border: 1px solid var(--yellow); }
        .status-badge.GREEN { background: rgba(34, 197, 94, 0.2); color: var(--green); border: 1px solid var(--green); }
        .status-badge.OFF { background: rgba(148, 163, 184, 0.15); color: var(--text-dim); border: 1px solid var(--border); }

        .info-row {
            display: flex;
            justify-content: space-around;
            background: var(--card-sub);
            border-radius: 12px;
            padding: 10px;
            margin-bottom: 20px;
            font-size: 0.8rem;
            border: 1px solid var(--border);
        }
        .info-item span { display: block; color: var(--text-dim); font-size: 0.7rem; margin-bottom: 2px; }
        .info-item strong { color: var(--text); }

        .buttons { display: grid; grid-template-columns: repeat(2, 1fr); gap: 10px; margin-bottom: 20px; }
        button {
            border: none;
            padding: 12px 14px;
            border-radius: 10px;
            font-weight: 600;
            font-size: 0.9rem;
            cursor: pointer;
            transition: all 0.2s;
            color: #fff;
        }
        button:hover { filter: brightness(1.15); transform: translateY(-1px); }
        button:active { transform: translateY(0); }
        .btn-green { background: #15803d; }
        .btn-yellow { background: #a16207; }
        .btn-red { background: #b91c1c; }
        .btn-auto { background: #4338ca; grid-column: span 2; }

        /* Device Section */
        .device-section {
            background: var(--card-sub);
            border: 1px solid var(--border);
            border-radius: 14px;
            padding: 16px;
            margin-bottom: 20px;
            text-align: left;
        }
        .device-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 12px;
        }
        .device-header h3 { font-size: 0.95rem; font-weight: 600; }
        .badge-paired {
            background: rgba(34, 197, 94, 0.15);
            color: var(--green);
            padding: 3px 8px;
            border-radius: 6px;
            font-size: 0.75rem;
            font-weight: 700;
        }
        .badge-unpaired {
            background: rgba(234, 179, 8, 0.15);
            color: var(--yellow);
            padding: 3px 8px;
            border-radius: 6px;
            font-size: 0.75rem;
            font-weight: 700;
        }
        .device-detail { font-size: 0.8rem; color: var(--text-dim); margin-bottom: 4px; }
        .device-detail strong { color: var(--text); }
        .device-actions { margin-top: 12px; display: flex; gap: 8px; }
        .btn-unpair {
            background: transparent;
            border: 1px solid var(--red);
            color: var(--red);
            padding: 6px 12px;
            font-size: 0.8rem;
            border-radius: 8px;
        }
        .btn-unpair:hover { background: rgba(239, 68, 68, 0.1); }
        .btn-scan {
            background: var(--blue);
            color: #fff;
            padding: 8px 14px;
            font-size: 0.85rem;
            border-radius: 8px;
            width: 100%;
        }

        .discovered-list {
            margin-top: 12px;
            display: flex;
            flex-direction: column;
            gap: 8px;
        }
        .device-card {
            background: rgba(255, 255, 255, 0.03);
            border: 1px solid var(--border);
            border-radius: 10px;
            padding: 10px 12px;
            display: flex;
            justify-content: space-between;
            align-items: center;
        }
        .device-card-info { font-size: 0.8rem; }
        .device-card-info div:first-child { font-weight: 600; color: var(--text); }
        .device-card-info div:last-child { color: var(--text-dim); font-size: 0.72rem; }
        .btn-pair-sm {
            background: var(--green);
            color: #fff;
            padding: 6px 12px;
            font-size: 0.75rem;
            border-radius: 6px;
        }

        /* Modal */
        .modal-overlay {
            display: none;
            position: fixed;
            top: 0; left: 0; right: 0; bottom: 0;
            background: rgba(0, 0, 0, 0.75);
            align-items: center;
            justify-content: center;
            z-index: 100;
        }
        .modal {
            background: var(--card-bg);
            border: 1px solid var(--border);
            border-radius: 16px;
            padding: 24px;
            width: 90%;
            max-width: 380px;
            text-align: left;
        }
        .modal h2 { font-size: 1.2rem; margin-bottom: 12px; }
        .form-group { margin-bottom: 14px; }
        .form-group label { display: block; font-size: 0.8rem; color: var(--text-dim); margin-bottom: 4px; }
        .form-group input {
            width: 100%;
            padding: 10px 12px;
            border-radius: 8px;
            border: 1px solid var(--border);
            background: #090d16;
            color: var(--text);
            font-size: 0.9rem;
        }
        .modal-buttons { display: flex; justify-content: flex-end; gap: 8px; margin-top: 18px; }
        .btn-cancel { background: #334155; padding: 8px 14px; font-size: 0.85rem; }
        .btn-confirm { background: var(--green); padding: 8px 14px; font-size: 0.85rem; }

        .shortcuts {
            color: var(--text-dim);
            font-size: 0.75rem;
            line-height: 1.5;
            background: rgba(0,0,0,0.2);
            padding: 8px;
            border-radius: 8px;
        }
        kbd {
            background: #334155;
            padding: 2px 5px;
            border-radius: 4px;
            color: #e2e8f0;
            font-family: monospace;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>SignalLight</h1>
        <p class="subtitle">Nano ESP32 Status Controller</p>

        <div class="traffic-light">
            <div id="bulb-red" class="bulb red"></div>
            <div id="bulb-yellow" class="bulb yellow"></div>
            <div id="bulb-green" class="bulb green"></div>
        </div>

        <div id="status-badge" class="status-badge OFF">DISCONNECTED (NO LIGHT)</div>

        <div class="info-row">
            <div class="info-item">
                <span>MODE</span>
                <strong id="info-mode">AUTO</strong>
            </div>
            <div class="info-item">
                <span>ZOOM</span>
                <strong id="info-zoom">OFFLINE</strong>
            </div>
            <div class="info-item">
                <span>LOCK</span>
                <strong id="info-lock">UNLOCKED</strong>
            </div>
            <div class="info-item">
                <span>CONTROLLER</span>
                <strong id="info-conn">DISCONNECTED</strong>
            </div>
        </div>

        <div class="buttons">
            <button class="btn-green" onclick="setColor('green')">🟢 Free</button>
            <button class="btn-yellow" onclick="setColor('yellow')">🟡 Away</button>
            <button class="btn-red" onclick="setColor('red')">🔴 Meeting</button>
            <button class="btn-auto" onclick="setColor('auto')">⚡ Auto Mode (Sync with Zoom)</button>
        </div>

        <!-- Light Identity & Pairing Panel -->
        <div class="device-section">
            <div class="device-header">
                <h3>Bluetooth Light Device</h3>
                <span id="device-paired-badge" class="badge-unpaired">UNPAIRED</span>
            </div>
            
            <div id="device-info-paired" style="display: none;">
                <div class="device-detail">Device Name: <strong id="cfg-name">-</strong></div>
                <div class="device-detail">MAC Address: <strong id="cfg-mac">-</strong></div>
                <div class="device-detail">Security: <strong id="cfg-sec">Shared Secret Handshake</strong></div>
                <div class="device-actions">
                    <button class="btn-unpair" onclick="unpairLight()">Unpair / Reset Light</button>
                </div>
            </div>

            <div id="device-info-unpaired">
                <p style="font-size: 0.8rem; color: var(--text-dim); margin-bottom: 10px;">
                    Scan nearby to discover your SignalLight, assign it a custom name, and lock it with a security secret.
                </p>
                <button id="btn-scan" class="btn-scan" onclick="scanLights()">🔍 Scan for Nearby Lights</button>
                <div id="scan-results" class="discovered-list"></div>
            </div>
        </div>

        <div class="shortcuts">
            <strong>Hotkeys:</strong> <kbd>Ctrl+Shift+G</kbd> Free | <kbd>Ctrl+Shift+Y</kbd> Away | <kbd>Ctrl+Shift+R</kbd> Busy | <kbd>Ctrl+Shift+A</kbd> Auto
        </div>
    </div>

    <!-- Pairing Modal -->
    <div id="pair-modal" class="modal-overlay">
        <div class="modal">
            <h2>Pair SignalLight</h2>
            <div class="form-group">
                <label>Target Hardware MAC</label>
                <input type="text" id="modal-mac" readonly style="opacity: 0.7;">
            </div>
            <div class="form-group">
                <label>Custom Device Name</label>
                <input type="text" id="modal-name" placeholder="e.g. Office Desk">
            </div>
            <div class="form-group">
                <label>Shared Security Secret / PIN</label>
                <input type="password" id="modal-secret" placeholder="Enter a secret password or PIN">
            </div>
            <div class="modal-buttons">
                <button class="btn-cancel" onclick="closeModal()">Cancel</button>
                <button class="btn-confirm" id="btn-submit-pair" onclick="submitPairing()">Save & Pair</button>
            </div>
        </div>
    </div>

    <script>
        async function fetchStatus() {
            try {
                const res = await fetch('/api/status');
                const data = await res.json();
                updateUI(data);
            } catch (e) {
                console.error(e);
            }
        }

        async function fetchConfig() {
            try {
                const res = await fetch('/api/ble/config');
                const cfg = await res.json();
                const badge = document.getElementById('device-paired-badge');
                if (cfg.paired && cfg.target_mac) {
                    badge.className = 'badge-paired';
                    badge.innerText = 'PAIRED';
                    document.getElementById('device-info-paired').style.display = 'block';
                    document.getElementById('device-info-unpaired').style.display = 'none';
                    document.getElementById('cfg-name').innerText = cfg.device_name || 'SignalLight';
                    document.getElementById('cfg-mac').innerText = cfg.target_mac;
                } else {
                    badge.className = 'badge-unpaired';
                    badge.innerText = 'UNPAIRED';
                    document.getElementById('device-info-paired').style.display = 'none';
                    document.getElementById('device-info-unpaired').style.display = 'block';
                }
            } catch (e) {
                console.error(e);
            }
        }

        function updateUI(data) {
            document.querySelectorAll('.bulb').forEach(b => b.classList.remove('active'));

            const badge = document.getElementById('status-badge');
            document.getElementById('info-mode').innerText = data.mode;
            document.getElementById('info-zoom').innerText = data.zoom_meeting ? 'IN MEETING' : 'NO MEETING';
            document.getElementById('info-lock').innerText = data.session_locked ? 'LOCKED' : 'UNLOCKED';

            if (!data.connected) {
                badge.className = 'status-badge OFF';
                badge.innerText = 'DISCONNECTED (NO LIGHT)';
                document.getElementById('info-conn').innerText = 'DISCONNECTED';
                return;
            }

            document.getElementById('info-conn').innerText = 'CONNECTED';
            const c = (data.color || '').toLowerCase();
            if (c === 'red') document.getElementById('bulb-red').classList.add('active');
            if (c === 'yellow') document.getElementById('bulb-yellow').classList.add('active');
            if (c === 'green') document.getElementById('bulb-green').classList.add('active');

            badge.className = 'status-badge ' + (data.color || 'OFF');
            let desc = data.color;
            if (data.color === 'RED') desc = 'IN MEETING (RED)';
            if (data.color === 'YELLOW') desc = 'AWAY / NOT AT DESK (YELLOW)';
            if (data.color === 'GREEN') desc = 'AVAILABLE (GREEN)';
            if (data.color === 'OFF') desc = 'LIGHT OFF';
            badge.innerText = desc;
        }

        async function setColor(color) {
            try {
                const res = await fetch('/api/set?color=' + color, { method: 'POST' });
                const data = await res.json();
                updateUI(data);
            } catch (e) {
                console.error(e);
            }
        }

        async function scanLights() {
            const btn = document.getElementById('btn-scan');
            const list = document.getElementById('scan-results');
            btn.disabled = true;
            btn.innerText = '⏳ Scanning nearby Bluetooth (6s)...';
            list.innerHTML = '';

            try {
                const res = await fetch('/api/ble/scan');
                const devices = await res.json();
                if (!devices || devices.length === 0) {
                    list.innerHTML = '<div style="font-size: 0.8rem; color: var(--text-dim); padding: 8px;">No SignalLights found nearby. Make sure your Arduino is powered on.</div>';
                } else {
                    devices.forEach(d => {
                        // Device name/address come from nearby BLE advertisements (untrusted,
                        // attacker-controllable radio input) — build the DOM with text nodes
                        // instead of innerHTML so a malicious advertised name can't inject markup.
                        const card = document.createElement('div');
                        card.className = 'device-card';

                        const info = document.createElement('div');
                        info.className = 'device-card-info';
                        const nameLine = document.createElement('div');
                        nameLine.textContent = d.name;
                        const detailLine = document.createElement('div');
                        detailLine.textContent = 'MAC: ' + d.address + ' | Signal: ' + d.rssi + ' dBm';
                        info.appendChild(nameLine);
                        info.appendChild(detailLine);

                        const pairBtn = document.createElement('button');
                        pairBtn.className = 'btn-pair-sm';
                        pairBtn.textContent = 'Pair';
                        pairBtn.addEventListener('click', () => openPairModal(d.address, d.name));

                        card.appendChild(info);
                        card.appendChild(pairBtn);
                        list.appendChild(card);
                    });
                }
            } catch (e) {
                list.innerHTML = '<div style="color: var(--red); font-size: 0.8rem;">Scan failed: ' + e + '</div>';
            } finally {
                btn.disabled = false;
                btn.innerText = '🔍 Scan for Nearby Lights';
            }
        }

        function openPairModal(mac, name) {
            document.getElementById('modal-mac').value = mac;
            document.getElementById('modal-name').value = name.startsWith('SignalLight-') ? 'Office Desk' : name;
            document.getElementById('modal-secret').value = '';
            document.getElementById('pair-modal').style.display = 'flex';
        }

        function closeModal() {
            document.getElementById('pair-modal').style.display = 'none';
        }

        async function submitPairing() {
            const mac = document.getElementById('modal-mac').value;
            const name = document.getElementById('modal-name').value.trim();
            const secret = document.getElementById('modal-secret').value.trim();

            if (!name || !secret) {
                alert('Please enter a custom device name and a security secret.');
                return;
            }

            const btn = document.getElementById('btn-submit-pair');
            btn.disabled = true;
            btn.innerText = 'Pairing...';

            try {
                const res = await fetch('/api/ble/pair', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ address: mac, name: name, secret: secret })
                });
                const data = await res.json();
                if (!res.ok || data.error) {
                    alert('Pairing failed: ' + (data.error || 'Unknown error'));
                } else {
                    closeModal();
                    fetchConfig();
                    fetchStatus();
                }
            } catch (e) {
                alert('Pairing request error: ' + e);
            } finally {
                btn.disabled = false;
                btn.innerText = 'Save & Pair';
            }
        }

        async function unpairLight() {
            if (!confirm('Are you sure you want to unpair this light? It will be reset to factory unpaired mode.')) {
                return;
            }
            try {
                await fetch('/api/ble/unpair', { method: 'POST' });
                fetchConfig();
                fetchStatus();
            } catch (e) {
                alert('Unpair failed: ' + e);
            }
        }

        setInterval(fetchStatus, 1000);
        fetchStatus();
        fetchConfig();
    </script>
</body>
</html>`
