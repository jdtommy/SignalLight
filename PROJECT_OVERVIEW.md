# SignalLight — Project Overview

An intelligent, wireless availability and presence indicator for home offices and shared workspaces.

---

## 🎯 Purpose & Philosophy

In modern remote and hybrid workspaces, interruptions during focused work or sensitive video meetings create friction. **SignalLight** solves this by physically broadcasting your real-time status to family members, housemates, or colleagues via a high-visibility, 3-color physical light:

- 🔴 **RED**: In a meeting or actively busy.
- 🟡 **YELLOW**: Away from desk or workstation locked.
- 🟢 **GREEN**: Free, available, and open to conversation.

The system is designed with a **zero-effort, automatic-first** philosophy:
- When a Zoom call starts, the light turns **RED** without touching a button.
- When you lock your screen (<kbd>Win</kbd> + <kbd>L</kbd>) or step away, the light turns **YELLOW**.
- When you unlock your screen, it seamlessly restores **GREEN**.
- Global hotkeys, a system tray icon, and a browser-based web dashboard provide instant manual overrides when needed.

---

## 🏗️ High-Level System Architecture

```
 +-------------------------------------------------------------------+
 |                        WINDOWS WORKSTATION                        |
 |                                                                   |
 |  [Zoom Meeting]     [Screen Lock]     [Global Hotkeys]            |
 |         │                 │                  │                    |
 |         ▼                 ▼                  ▼                    |
 |  ┌─────────────────────────────────────────────────────────────┐  |
 |  │                State Manager (windows/state)                │  |
 |  │     Priority: Zoom (RED) > Lock (YELLOW) > Free (GREEN)     │  |
 |  └──────────────────────────────┬──────────────────────────────┘  |
 |                                 │                                 |
 |                                 ▼                                 |
 |  ┌─────────────────────────────────────────────────────────────┐  |
 |  │              BLE Central Client (windows/ble)               │  |
 |  │        Auto-discovery, Security Handshake, Heartbeats       │  |
 |  └──────────────────────────────┬──────────────────────────────┘  |
 |                                 │                                 |
 |  [Web Dashboard (localhost:9439)] [Tray Icon (Green/Yellow/Red)]  |
 +─────────────────────────────────┼─────────────────────────────────+
                                   │
                                   │ Bluetooth Low Energy (2.4 GHz)
                                   ▼
 +-------------------------------------------------------------------+
 |                    SIGNAL LIGHT HARDWARE UNIT                     |
 |                                                                   |
 |  ┌─────────────────────────────────────────────────────────────┐  |
 |  │              Arduino Nano ESP32 (ESP32-S3)                  │  |
 |  │    - BLE Peripheral Service (19B10000-...)                  │  |
 |  │    - Flash Persistence (NVS) for Pairing & Color Memory     │  |
 |  │    - 45s Disconnect Grace Period & Failsafe Watchdog        │  |
 |  └──────────┬───────────────────┬───────────────────┬──────────┘  |
 |             │ (Pin D2: RED)     │ (Pin D3: YELLOW)  │ (Pin D4: GREEN)
 |             ▼                   ▼                   ▼             |
 |      [MOSFET Driver]     [MOSFET Driver]     [MOSFET Driver]      |
 |             │                   │                   │             |
 |             ▼                   ▼                   ▼             |
 |       🔴 RED LIGHT        🟡 YELLOW LIGHT     🟢 GREEN LIGHT      |
 |                                                                   |
 +-------------------------------------------------------------------+
```

---

## 📦 Repository Structure

```
SignalLight/
├── AGENTS.md                    # Detailed guide and rules for AI assistants
├── ARCHITECTURE.md              # Technical protocols, state diagrams & security
├── PROJECT_OVERVIEW.md          # Comprehensive project description (this file)
├── README.md                    # Quickstart and user manual
│
├── arduino/
│   ├── SignalLight/
│   │   └── SignalLight.ino      # Main Arduino C++ firmware (ESP32-S3 BLE)
│   └── micropython/
│       └── main.py              # MicroPython reference firmware
│
└── windows/
    ├── ble/                     # Windows WinRT BLE client & connection lifecycle
    ├── config/                  # Configuration manager (%APPDATA%\SignalLight)
    ├── hotkey/                  # Windows global hotkey listener
    ├── serial/                  # USB Serial communication fallback
    ├── server/                  # Embedded HTTP server & Web Dashboard UI
    ├── session/                 # Windows workstation lock/unlock detector
    ├── state/                   # Central availability state machine
    ├── third_party/bluetooth/   # Forked bluetooth library with WinRT timeout patch
    ├── tray/                    # Dynamic Windows system tray notification icon
    ├── zoom/                    # Zero-config Win32 Zoom meeting window detector
    ├── main.go                  # Windows entry point
    ├── run.bat / run.ps1        # One-click launch scripts (source execution)
    └── signallight.exe          # Standalone compiled executable
```

---

## 💡 Key Features

### 1. Hands-Free Zoom Sync
No Zoom OAuth or cloud tokens required. The Windows desktop daemon monitors native Zoom video window handles (`ZPContentViewWnd`, `ZPFloatVideoWndClass`) and window titles. As soon as you join a meeting or breakout room, the light switches to Red in less than a second.

### 2. Workstation Lock Detection
Monitors Windows session changes (`WM_WTSSESSION_CHANGE`). When you lock your screen (<kbd>Win</kbd> + <kbd>L</kbd>) or your PC locks after idle timeout, the light switches to Yellow (Away). Returning and unlocking your screen restores Green (Available).

### 3. Resilient BLE with Grace Period
- **45-Second Disconnect Grace:** If Bluetooth experiences momentary RF interference or your laptop renegotiates connection parameters, the light stays solid Green. It only switches to Yellow if disconnected continuously for more than 45 seconds.
- **Instant Reconnection:** When your laptop comes back into range, the Arduino automatically and immediately restores the active color.
- **Fail-Safe Watchdog:** If your PC shuts down, the Arduino detects the absence of heartbeats and falls back to Yellow.

### 4. Interactive Web Dashboard
A modern, responsive web control interface runs on a dynamically allocated local port (e.g. `http://localhost:9439`). Features:
- Real-time live status indicator.
- Single-click color buttons (Green, Yellow, Red, Off).
- Auto mode toggle.
- BLE device scanner, pairing wizard, and unpair controls.

### 5. Multi-Color System Tray Applet
The application lives quietly in the Windows system tray with a dynamic icon colored Green, Yellow, or Red matching your real-time status. Right-clicking provides instant manual controls and direct dashboard access.

---

## ⚡ Hardware Specifications

| Component | Specification |
|-----------|---------------|
| **Microcontroller** | Arduino Nano ESP32 (ESP32-S3, Xtensa dual-core, 2.4 GHz BLE) |
| **Operating Voltage** | 5V via USB-C or VBUS; internal MCU logic is **strictly 3.3V** |
| **Logic Outputs** | `D2` (Red), `D3` (Yellow), `D4` (Green) at 3.3V logic level |
| **Power Switching** | Low-side N-Channel Logic MOSFETs (e.g., IRLZ44N, 2N7000, AO3400) |
| **External Light** | 12V LED indicator tower, lamp, or 12V LED strip segments |
| **Safety Circuit** | 10kΩ pull-up resistor from Yellow MOSFET Gate to **3.3V rail** (ensures Yellow on startup/power-off) |

---

## 🚀 Getting Started

1. **Flash the Arduino:** Open [`arduino/SignalLight/SignalLight.ino`](file:///C:/Users/jdtom/dev/SignalLight/arduino/SignalLight/SignalLight.ino) in the Arduino IDE and upload to your Arduino Nano ESP32.
2. **Launch the Windows App:**
   ```powershell
   cd C:\Users\jdtom\dev\SignalLight\windows
   .\run.bat
   ```
3. **Open Dashboard:** Navigate to `http://localhost:9439` (or the port displayed in the console) to complete initial pairing.
