package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io"
	"log"
	"net/http"
	"strings"

	"signallight/state"
)

type Server struct {
	addr        string
	stateMgr    *state.Manager
	zoomSecret  string // Optional Zoom webhook secret token
	mux         *http.ServeMux
	httpServer  *http.Server
}

func NewServer(addr string, stateMgr *state.Manager, zoomSecret string) *Server {
	s := &Server{
		addr:       addr,
		stateMgr:   stateMgr,
		zoomSecret: zoomSecret,
		mux:        http.NewServeMux(),
	}
	s.routes()
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: s.mux,
	}
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleDashboard)
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/api/set", s.handleSet)
	s.mux.HandleFunc("/webhook/zoom", s.handleZoomWebhook)
}

func (s *Server) Start() error {
	log.Printf("[Web] SignalLight web interface listening at http://localhost%s", s.addr)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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

	// Zoom Webhook URL Validation challenge
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

	log.Printf("[Zoom Webhook] Received event: %s", payload.Event)

	switch payload.Event {
	case "meeting.started", "meeting.participant_joined":
		s.stateMgr.OnZoomMeetingChanged(true)
	case "meeting.ended", "meeting.participant_left":
		s.stateMgr.OnZoomMeetingChanged(false)
	case "user.presence_status_updated":
		status := strings.ToLower(payload.Payload.Object.PresenceStatus)
		if status == "in_meeting" || status == "do_not_disturb" {
			s.stateMgr.OnZoomMeetingChanged(true)
		} else if status == "available" {
			s.stateMgr.OnZoomMeetingChanged(false)
		} else if status == "away" {
			s.stateMgr.SetManualColor(state.ColorYellow)
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
            --text: #f8fafc;
            --text-dim: #94a3b8;
            --red: #ef4444;
            --yellow: #eab308;
            --green: #22c55e;
            --gray: #64748b;
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
            padding: 20px;
        }
        .container {
            background: var(--card-bg);
            border-radius: 20px;
            padding: 32px;
            width: 100%;
            max-width: 480px;
            box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.5);
            border: 1px solid rgba(255, 255, 255, 0.08);
            text-align: center;
        }
        h1 { font-size: 1.6rem; margin-bottom: 6px; font-weight: 700; letter-spacing: -0.5px; }
        .subtitle { color: var(--text-dim); font-size: 0.9rem; margin-bottom: 24px; }
        
        .traffic-light {
            display: flex;
            flex-direction: column;
            gap: 14px;
            background: #090d16;
            padding: 20px;
            border-radius: 50px;
            width: 100px;
            margin: 0 auto 24px;
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
            font-size: 1.1rem;
            font-weight: 700;
            padding: 8px 18px;
            border-radius: 9999px;
            margin-bottom: 16px;
            text-transform: uppercase;
            letter-spacing: 1px;
        }
        .status-badge.RED { background: rgba(239, 68, 68, 0.2); color: var(--red); border: 1px solid var(--red); }
        .status-badge.YELLOW { background: rgba(234, 179, 8, 0.2); color: var(--yellow); border: 1px solid var(--yellow); }
        .status-badge.GREEN { background: rgba(34, 197, 94, 0.2); color: var(--green); border: 1px solid var(--green); }

        .info-row {
            display: flex;
            justify-content: space-around;
            background: rgba(15, 23, 42, 0.6);
            border-radius: 12px;
            padding: 12px;
            margin-bottom: 24px;
            font-size: 0.85rem;
        }
        .info-item span { display: block; color: var(--text-dim); font-size: 0.75rem; margin-bottom: 2px; }
        .info-item strong { color: var(--text); }

        .buttons { display: grid; grid-template-columns: repeat(2, 1fr); gap: 10px; margin-bottom: 20px; }
        button {
            border: none;
            padding: 14px 16px;
            border-radius: 12px;
            font-weight: 600;
            font-size: 0.95rem;
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

        .shortcuts {
            color: var(--text-dim);
            font-size: 0.75rem;
            line-height: 1.5;
            background: rgba(0,0,0,0.2);
            padding: 10px;
            border-radius: 8px;
        }
        kbd {
            background: #334155;
            padding: 2px 6px;
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
            <div id="bulb-yellow" class="bulb yellow active"></div>
            <div id="bulb-green" class="bulb green"></div>
        </div>

        <div id="status-badge" class="status-badge YELLOW">AWAY (YELLOW)</div>

        <div class="info-row">
            <div class="info-item">
                <span>MODE</span>
                <strong id="info-mode">AUTO</strong>
            </div>
            <div class="info-item">
                <span>ZOOM MEETING</span>
                <strong id="info-zoom">OFFLINE</strong>
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

        <div class="shortcuts">
            <strong>Hotkeys:</strong> <kbd>Ctrl+Shift+G</kbd> Free | <kbd>Ctrl+Shift+Y</kbd> Away | <kbd>Ctrl+Shift+R</kbd> Busy | <kbd>Ctrl+Shift+A</kbd> Auto
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

        function updateUI(data) {
            document.querySelectorAll('.bulb').forEach(b => b.classList.remove('active'));
            const c = data.color.toLowerCase();
            if (c === 'red') document.getElementById('bulb-red').classList.add('active');
            if (c === 'yellow') document.getElementById('bulb-yellow').classList.add('active');
            if (c === 'green') document.getElementById('bulb-green').classList.add('active');

            const badge = document.getElementById('status-badge');
            badge.className = 'status-badge ' + data.color;
            let desc = data.color;
            if (data.color === 'RED') desc = 'IN MEETING (RED)';
            if (data.color === 'YELLOW') desc = 'AWAY / NOT AT DESK (YELLOW)';
            if (data.color === 'GREEN') desc = 'AVAILABLE (GREEN)';
            badge.innerText = desc;

            document.getElementById('info-mode').innerText = data.mode;
            document.getElementById('info-zoom').innerText = data.zoom_meeting ? 'IN MEETING' : 'NO MEETING';
            document.getElementById('info-conn').innerText = data.connected ? 'CONNECTED' : 'CONNECTING...';
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

        setInterval(fetchStatus, 1000);
        fetchStatus();
    </script>
</body>
</html>`
