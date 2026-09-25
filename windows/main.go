package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"signallight/ble"
	"signallight/config"
	"signallight/hotkey"
	"signallight/serial"
	"signallight/server"
	"signallight/session"
	"signallight/state"
	"signallight/tray"
	"signallight/zoom"

	"github.com/energye/systray"
)

func main() {
	useBle := flag.Bool("ble", true, "Connect to Arduino via Bluetooth Low Energy (default true)")
	targetMAC := flag.String("mac", "", "Target BLE MAC address (e.g. E8:F6:0A:BE:69:F5, overrides config)")
	bleDeviceName := flag.String("device", "", "Target BLE device advertisement name (overrides config)")
	secretFlag := flag.String("secret", "", "Shared security secret / PIN (overrides config)")
	serialPort := flag.String("serial", "", "Serial COM port to use (e.g. COM3 or 'auto', empty to disable)")
	httpPort := flag.String("port", "", "HTTP port for web dashboard & API (default: auto-assign open port)")
	useTray := flag.Bool("tray", true, "Enable Windows system tray icon (default true)")
	zoomSecret := flag.String("zoom-secret", "", "Zoom webhook verification secret (optional)")
	checkInterval := flag.Duration("interval", 1*time.Second, "Zoom polling interval")
	flag.Parse()

	// Load stored configuration from %APPDATA%\SignalLight\config.json.
	// Load() always returns a non-nil config (an empty/unpaired default on error),
	// so a corrupt or unreadable file degrades to "unpaired" instead of crashing.
	cfg, err := config.Load()
	if err != nil {
		log.Printf("[Config] Warning: error reading config, starting unpaired: %v", err)
	}

	// Command-line flag overrides
	if *targetMAC != "" {
		cfg.TargetMAC = strings.ToUpper(strings.TrimSpace(*targetMAC))
	}
	if *bleDeviceName != "" {
		cfg.DeviceName = strings.TrimSpace(*bleDeviceName)
	}
	if *secretFlag != "" {
		cfg.SharedSecret = strings.TrimSpace(*secretFlag)
	}

	// Resolve open port and listener for the web server
	listener, portNum, err := server.ResolveListener(*httpPort, cfg)
	if err != nil {
		log.Fatalf("[Web] Fatal error resolving port: %v", err)
	}

	displayHost := fmt.Sprintf("localhost:%d", portNum)

	fmt.Println("==================================================")
	fmt.Println("        SignalLight Controller for Windows        ")
	fmt.Println("==================================================")
	if cfg.Paired && cfg.TargetMAC != "" {
		fmt.Printf("Paired Device: %s (%s)\n", cfg.DeviceName, cfg.TargetMAC)
	} else {
		fmt.Println("Paired Device: None (Open Web Dashboard to pair)")
	}
	fmt.Println("Config File:   " + config.GetConfigPath())
	fmt.Println("Hotkeys:")
	fmt.Println("  [Ctrl + Shift + G] -> Set GREEN  (Available / Free)")
	fmt.Println("  [Ctrl + Shift + Y] -> Set YELLOW (Away / Not at Desk)")
	fmt.Println("  [Ctrl + Shift + R] -> Set RED    (In Meeting / Busy)")
	fmt.Println("  [Ctrl + Shift + A] -> Set AUTO   (Sync with Zoom)")
	fmt.Println("Web Dashboard: http://" + displayHost)
	fmt.Println("==================================================")

	stateMgr := state.NewManager()

	var bleClient *ble.Client
	var serialClient *serial.Client

	// Communication sender callback
	sendColorToHardware := func(color state.LightColor) {
		colStr := string(color)
		if bleClient != nil && bleClient.IsConnected() {
			bleClient.SendColor(colStr)
		}
		if serialClient != nil && serialClient.IsConnected() {
			serialClient.SendColor(colStr)
		}
	}

	// Subscribe hardware sender to state changes
	stateMgr.Subscribe(func(st state.Status) {
		log.Printf("[State] Color: %s | Mode: %s | ZoomMeeting: %v | SessionLocked: %v | Connected: %v",
			st.Color, st.Mode, st.ZoomMeeting, st.SessionLocked, st.Connected)
		sendColorToHardware(st.Color)
	})

	// Start BLE client if enabled
	if *useBle {
		bleClient = ble.NewClient(
			cfg.TargetMAC,
			cfg.DeviceName,
			cfg.SharedSecret,
			func() {
				stateMgr.SetConnected(true)
				curr := stateMgr.GetStatus()
				sendColorToHardware(curr.Color)
			},
			func() {
				// Only mark disconnected if serial is also not connected
				if serialClient == nil || !serialClient.IsConnected() {
					stateMgr.SetConnected(false)
				}
			},
		)

		bleClient.SetOnUnpaired(func() {
			log.Println("[BLE] Device announced it is UNPAIRED (hardware reset). Clearing local config.")
			_ = config.Clear()
			stateMgr.SetConnected(false)
		})

		if err := bleClient.Start(); err != nil {
			log.Printf("[BLE] Failed to initialize BLE: %v", err)
			log.Printf("[BLE] If using USB cable instead, run with -ble=false -serial=auto")
		}
	}

	// Start Serial client if configured
	if *serialPort != "" {
		serialClient = serial.NewClient(
			*serialPort,
			func() {
				stateMgr.SetConnected(true)
				curr := stateMgr.GetStatus()
				sendColorToHardware(curr.Color)
			},
			func() {
				if bleClient == nil || !bleClient.IsConnected() {
					stateMgr.SetConnected(false)
				}
			},
		)
		serialClient.Start()
	}

	// Start Zoom meeting detector
	detector := zoom.NewDetector(*checkInterval, func(inMeeting bool) {
		log.Printf("[Zoom] Meeting status changed: inMeeting=%v", inMeeting)
		stateMgr.OnZoomMeetingChanged(inMeeting)
	})
	detector.Start()
	log.Println("[Zoom] Meeting detector started.")

	// Start Windows Session Lock watcher
	sessionWatcher := session.NewWatcher(func(locked bool) {
		log.Printf("[Session] Lock state changed: locked=%v", locked)
		stateMgr.OnSessionLockChanged(locked)
	})
	if err := sessionWatcher.Start(); err != nil {
		log.Printf("[Session] Warning: failed to start session lock watcher: %v", err)
	} else {
		log.Println("[Session] Session lock watcher started.")
	}

	// Start Global Hotkeys
	hkListener := hotkey.NewListener(func(action hotkey.Action) {
		switch action {
		case hotkey.ActionRed:
			log.Println("[Hotkey] Pressed: RED")
			stateMgr.SetManualColor(state.ColorRed)
		case hotkey.ActionYellow:
			log.Println("[Hotkey] Pressed: YELLOW")
			stateMgr.SetManualColor(state.ColorYellow)
		case hotkey.ActionGreen:
			log.Println("[Hotkey] Pressed: GREEN")
			stateMgr.SetManualColor(state.ColorGreen)
		case hotkey.ActionAuto:
			log.Println("[Hotkey] Pressed: AUTO")
			stateMgr.SetAutoMode()
		}
	})
	if err := hkListener.Start(); err != nil {
		log.Printf("[Hotkey] Warning: failed to register hotkeys: %v", err)
	} else {
		log.Println("[Hotkey] Global hotkeys registered successfully.")
	}

	// Start Web Server
	webSrv := server.NewServer(displayHost, stateMgr, bleClient, *zoomSecret)
	if err := webSrv.Start(listener); err != nil {
		log.Printf("[Web] Failed to start server: %v", err)
	}

	cleanup := func() {
		log.Println("\nShutting down SignalLight...")
		detector.Stop()
		sessionWatcher.Stop()
		if listener != nil {
			_ = listener.Close()
		}
		if bleClient != nil {
			bleClient.Stop()
		}
		if serialClient != nil {
			serialClient.Stop()
		}
		log.Println("Goodbye!")
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	if *useTray {
		onReady, onExit := tray.SetupTray(stateMgr, displayHost, cleanup)
		go func() {
			<-sigChan
			systray.Quit()
		}()
		log.Println("[Tray] Starting system tray icon...")
		systray.Run(onReady, onExit)
	} else {
		<-sigChan
		cleanup()
	}
}
