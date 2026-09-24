package tray

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os/exec"
	"strings"

	"github.com/energye/systray"
	"signallight/state"
)

var (
	iconGreen  []byte
	iconYellow []byte
	iconRed    []byte
	iconGray   []byte
)

func init() {
	iconGreen = createCircleIcon(color.RGBA{R: 34, G: 197, B: 94, A: 255})
	iconYellow = createCircleIcon(color.RGBA{R: 234, G: 179, B: 8, A: 255})
	iconRed = createCircleIcon(color.RGBA{R: 239, G: 68, B: 68, A: 255})
	iconGray = createCircleIcon(color.RGBA{R: 100, G: 116, B: 139, A: 255})
}

// createCircleIcon generates a Windows .ico binary in-memory with a colored circle.
func createCircleIcon(c color.RGBA) []byte {
	const size = 32
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	center := float64(size) / 2.0
	radius := float64(size)/2.0 - 2.0

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x) + 0.5 - center
			dy := float64(y) + 0.5 - center
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist <= radius {
				img.Set(x, y, c)
			} else if dist <= radius+1.0 {
				alpha := uint8(float64(c.A) * (radius + 1.0 - dist))
				img.Set(x, y, color.RGBA{R: c.R, G: c.G, B: c.B, A: alpha})
			}
		}
	}

	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, img)
	pngBytes := pngBuf.Bytes()

	var icoBuf bytes.Buffer
	// ICO Header
	_ = binary.Write(&icoBuf, binary.LittleEndian, uint16(0)) // Reserved
	_ = binary.Write(&icoBuf, binary.LittleEndian, uint16(1)) // Type 1 = Icon
	_ = binary.Write(&icoBuf, binary.LittleEndian, uint16(1)) // 1 image

	// Directory Entry
	icoBuf.WriteByte(byte(size))                                       // Width
	icoBuf.WriteByte(byte(size))                                       // Height
	icoBuf.WriteByte(0)                                                // Colors
	icoBuf.WriteByte(0)                                                // Reserved
	_ = binary.Write(&icoBuf, binary.LittleEndian, uint16(1))          // Color planes
	_ = binary.Write(&icoBuf, binary.LittleEndian, uint16(32))         // Bits per pixel
	_ = binary.Write(&icoBuf, binary.LittleEndian, uint32(len(pngBytes))) // Size of PNG
	_ = binary.Write(&icoBuf, binary.LittleEndian, uint32(22))         // Offset of PNG data

	icoBuf.Write(pngBytes)
	return icoBuf.Bytes()
}

// SetupTray initializes the system tray icon and sets up menu handlers.
func SetupTray(stateMgr *state.Manager, webPort string, onQuit func()) (func(), func()) {
	onReady := func() {
		systray.SetIcon(iconYellow)
		systray.SetTooltip("SignalLight: Initializing...")

		// Header (read-only indicator)
		mHeader := systray.AddMenuItem("SignalLight - Connecting...", "")
		mHeader.Disable()

		systray.AddSeparator()

		// Quick status buttons
		mGreen := systray.AddMenuItemCheckbox("🟢 Available (Green)", "Set Available", false)
		mYellow := systray.AddMenuItemCheckbox("🟡 Away (Yellow)", "Set Away", true)
		mRed := systray.AddMenuItemCheckbox("🔴 Busy / Meeting (Red)", "Set Busy", false)
		mAuto := systray.AddMenuItemCheckbox("⚡ Auto Mode (Sync Zoom)", "Automatically follow Zoom meeting status", true)

		systray.AddSeparator()

		// Dashboard link
		dashHost := strings.TrimSpace(webPort)
		if dashHost == "" {
			dashHost = ":8080"
		} else if !strings.Contains(dashHost, ":") {
			dashHost = ":" + dashHost
		}
		if strings.HasPrefix(dashHost, ":") {
			dashHost = "localhost" + dashHost
		}
		dashboardURL := "http://" + dashHost
		mDashboard := systray.AddMenuItem("🌐 Open Web Dashboard", "Open "+dashboardURL)

		systray.AddSeparator()

		// Quit
		mQuit := systray.AddMenuItem("❌ Exit SignalLight", "Quit application")

		// Helper to update menu checks
		updateUI := func(st state.Status) {
			defer func() {
				_ = recover()
			}()

			switch st.Color {
			case state.ColorGreen:
				systray.SetIcon(iconGreen)
				systray.SetTooltip(fmt.Sprintf("SignalLight: Available (Green) [%s]", st.Mode))
				mHeader.SetTitle(fmt.Sprintf("Status: Available (Green) [%s]", st.Mode))
				mGreen.Check()
				mYellow.Uncheck()
				mRed.Uncheck()
			case state.ColorRed:
				systray.SetIcon(iconRed)
				systray.SetTooltip(fmt.Sprintf("SignalLight: Busy / Meeting (Red) [%s]", st.Mode))
				mHeader.SetTitle(fmt.Sprintf("Status: Busy / Meeting (Red) [%s]", st.Mode))
				mRed.Check()
				mGreen.Uncheck()
				mYellow.Uncheck()
			case state.ColorYellow:
				systray.SetIcon(iconYellow)
				systray.SetTooltip(fmt.Sprintf("SignalLight: Away (Yellow) [%s]", st.Mode))
				mHeader.SetTitle(fmt.Sprintf("Status: Away (Yellow) [%s]", st.Mode))
				mYellow.Check()
				mGreen.Uncheck()
				mRed.Uncheck()
			default:
				systray.SetIcon(iconGray)
				systray.SetTooltip(fmt.Sprintf("SignalLight: %s [%s]", st.Color, st.Mode))
				mHeader.SetTitle(fmt.Sprintf("Status: %s [%s]", st.Color, st.Mode))
			}

			if st.Mode == state.ModeAuto {
				mAuto.Check()
			} else {
				mAuto.Uncheck()
			}
		}

		// Subscribe to state manager updates
		stateMgr.Subscribe(func(st state.Status) {
			updateUI(st)
		})

		// Trigger initial update
		updateUI(stateMgr.GetStatus())

		// Handle menu item clicks
		mGreen.Click(func() {
			log.Println("[Tray] Clicked: Available (Green)")
			stateMgr.SetManualColor(state.ColorGreen)
		})

		mYellow.Click(func() {
			log.Println("[Tray] Clicked: Away (Yellow)")
			stateMgr.SetManualColor(state.ColorYellow)
		})

		mRed.Click(func() {
			log.Println("[Tray] Clicked: Busy / Meeting (Red)")
			stateMgr.SetManualColor(state.ColorRed)
		})

		mAuto.Click(func() {
			log.Println("[Tray] Clicked: Auto Mode")
			stateMgr.SetAutoMode()
		})

		mDashboard.Click(func() {
			log.Printf("[Tray] Opening web dashboard: %s", dashboardURL)
			_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", dashboardURL).Start()
		})

		mQuit.Click(func() {
			log.Println("[Tray] Exit clicked")
			systray.Quit()
		})

		// Open menu on left or right click on tray icon
		systray.SetOnClick(func(menu systray.IMenu) {
			_ = menu.ShowMenu()
		})
		systray.SetOnRClick(func(menu systray.IMenu) {
			_ = menu.ShowMenu()
		})
	}

	onExit := func() {
		log.Println("[Tray] System tray exiting")
		if onQuit != nil {
			onQuit()
		}
	}

	return onReady, onExit
}
