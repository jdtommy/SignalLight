package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"signallight/ble"
	"signallight/hotkey"
	"signallight/serial"
	"signallight/server"
	"signallight/state"
	"signallight/zoom"
)

func main() {
	useBle := flag.Bool("ble", true, "Connect to Arduino via Bluetooth Low Energy (default true)")
	bleDeviceName := flag.String("device", "SignalLight", "BLE device advertisement name")
	serialPort := flag.String("serial", "", "Serial COM port to use (e.g. COM3 or 'auto', empty to disable)")
	httpPort := flag.String("port", ":8080", "HTTP port for web dashboard & API")
	zoomSecret := flag.String("zoom-secret", "", "Zoom webhook verification secret (optional)")
	checkInterval := flag.Duration("interval", 1*time.Second, "Zoom polling interval")
	flag.Parse()

	fmt.Println("==================================================")
	fmt.Println("        SignalLight Controller for Windows        ")
	fmt.Println("==================================================")
	fmt.Println("Hotkeys:")
	fmt.Println("  [Ctrl + Shift + G] -> Set GREEN  (Available / Free)")
	fmt.Println("  [Ctrl + Shift + Y] -> Set YELLOW (Away / Not at Desk)")
	fmt.Println("  [Ctrl + Shift + R] -> Set RED    (In Meeting / Busy)")
	fmt.Println("  [Ctrl + Shift + A] -> Set AUTO   (Sync with Zoom)")
	fmt.Println("Web Dashboard: http://localhost" + *httpPort)
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
		log.Printf("[State] Color: %s | Mode: %s | ZoomMeeting: %v", st.Color, st.Mode, st.ZoomMeeting)
		sendColorToHardware(st.Color)
	})

	// Start BLE client if enabled
	if *useBle {
		bleClient = ble.NewClient(
			*bleDeviceName,
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
	webSrv := server.NewServer(*httpPort, stateMgr, *zoomSecret)
	if err := webSrv.Start(); err != nil {
		log.Printf("[Web] Failed to start server: %v", err)
	}

	// Wait for termination signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("\nShutting down SignalLight...")
	detector.Stop()
	if bleClient != nil {
		bleClient.Stop()
	}
	if serialClient != nil {
		serialClient.Stop()
	}
	log.Println("Goodbye!")
}
