# AGENTS.md — SignalLight AI Engineering & Agent Guide

> **Target Audience:** AI coding assistants (Antigravity, Claude, Copilot, Cursor, ChatGPT, etc.) working on the SignalLight repository.
> **Purpose:** Provides critical operational context, architectural rules, hardware safety requirements, communication protocols, and code modification guidelines to prevent regressions.

---

## 🚦 Project Summary

**SignalLight** is an automated office availability indicator system composed of:
1. **Embedded Firmware** (`arduino/SignalLight/SignalLight.ino`): Runs on an **Arduino Nano ESP32 (ESP32-S3)**. Drives external 12V status LEDs (Red, Yellow, Green) via logic-level MOSFETs and mirrors state onto the onboard RGB LED. Communicates wirelessly via Bluetooth Low Energy (BLE) or optionally via USB Serial.
2. **Windows Desktop Companion** (`windows/`): A **Go (Golang)** daemon and dashboard running in the Windows system tray. Automatically monitors Zoom meeting states and workstation lock screens, manages BLE connection and heartbeats, exposes global hotkeys and a local web dashboard, and syncs color states to the hardware in real time.

---

## ⚠️ Critical Safety & Hardware Rules

### 1. Strictly 3.3V Logic on the ESP32-S3
- The ESP32-S3 microcontroller GPIOs (`D2`, `D3`, `D4`) are **strictly 3.3V**. They are **NOT 5V tolerant** (absolute maximum voltage is 3.6V).
- **Never** connect pull-up resistors to 5V or `VBUS`. All pull-ups must go to the **`3V3`** rail.
- External 12V LED strips/bulbs are switched on the low side via **Logic-Level N-Channel MOSFETs** (e.g. `IRLZ44N`, `2N7000`, `AO3400`).

### 2. Physical Pinout Mapping
| Microcontroller Pin | Function | Logic Level | Hardware Controlled |
|---------------------|----------|-------------|---------------------|
| `D2` | RED Output | 3.3V Active-HIGH | Red LED (Busy / Meeting) |
| `D3` | YELLOW Output | 3.3V Active-HIGH | Yellow LED (Away / Default) |
| `D4` | GREEN Output | 3.3V Active-HIGH | Green LED (Available / Free) |
| `LED_RED` | Onboard RGB Red | Active-LOW | Arduino Nano ESP32 Onboard RGB |
| `LED_GREEN` | Onboard RGB Green | Active-LOW | Arduino Nano ESP32 Onboard RGB |
| `LED_BLUE` | Onboard RGB Blue | Active-LOW | Arduino Nano ESP32 Onboard RGB |
| `LED_BUILTIN` | Onboard Yellow LED | Active-HIGH | Arduino Nano ESP32 Builtin LED |

---

## 📡 Wireless & BLE Protocol Specification

### BLE Identifiers
- **Service UUID:** `19B10000-E8F2-537E-4F6C-D104768A1214`
- **Control Characteristic UUID:** `19B10001-E8F2-537E-4F6C-D104768A1214`
  - Permissions: `BLERead | BLEWrite | BLEWriteWithoutResponse | BLENotify` (1 byte)
- **Auth Characteristic UUID:** `19B10002-E8F2-537E-4F6C-D104768A1214`
  - Permissions: `BLERead | BLEWrite | BLENotify` (string, max 64 bytes)

### Security & Pairing Model
- **Unpaired Mode:** Advertises as `SignalLight-[Last 4 MAC]` (e.g. `SignalLight-69F5`). Onboard/external Yellow LED pulses slowly.
- **Pairing Handshake:** Central writes `PAIR:<FriendlyName>:<SharedSecret>` to Auth characteristic within 4 seconds. Returns `PAIR_OK` and stores credentials to NVS flash (`Preferences`).
- **Paired Mode:** Advertises as `<FriendlyName>` (e.g. `Jarads8`). Requires `AUTH:<SharedSecret>` within 4 seconds of connection.
- **Factory Reset Options:**
  1. Write `UNPAIR` to Auth characteristic.
  2. Send `FACTORY_RESET` over USB Serial.
  3. Physical Double-Press: Press hardware reset button twice within 4 seconds of boot (`rst_armed` flag in flash).

### Control Commands (`19B10001`)
| Command Byte | Description | Behavior |
|--------------|-------------|----------|
| `'R'` | In Meeting / Busy | Sets `activeColor = 'R'`, drives D2 HIGH, illuminates Onboard Red |
| `'Y'` | Away / Paused | Sets `activeColor = 'Y'`, drives D3 HIGH, illuminates Onboard Yellow |
| `'G'` | Available / Free | Sets `activeColor = 'G'`, drives D4 HIGH, illuminates Onboard Green |
| `'0'` | All OFF | Turns off all external LEDs and onboard RGB |
| `'P'` | Heartbeat Ping | Updates `lastHeartbeatTime`. If displaying Yellow due to previous timeout, restores `activeColor` |
| `'?'` | Status Query | Prints current `displayedColor` to USB Serial (`STATUS:G`) |

---

## 🔄 State Machine & Reconnect Architecture

### Arduino State Separation
To eliminate flickering and unintended fallback to Yellow, the firmware maintains two distinct variables:
1. `activeColor`: The intended operational state set by the user or desktop app (`'R'`, `'G'`, `'Y'`, `'0'`). Persisted in NVS flash partition (`signallight/active_color`).
2. `displayedColor`: The actual physical state currently energized on the GPIO pins.

### Watchdog & Disconnect Grace Periods
- **Disconnect Grace Period (`DISCONNECT_GRACE_MS = 45000`):**
  - If the BLE link drops momentarily (1–5 seconds for renegotiation or background sleep), **the Arduino does NOT turn Yellow**. It continues illuminating `activeColor` (`GREEN`).
  - Only if disconnected continuously for > 45 seconds does it switch `displayedColor` to Yellow.
  - When reconnected and authenticated (`AUTH_OK`), the Arduino **immediately restores `activeColor`**, eliminating any yellow glitch.
- **Connected Watchdog (`HEARTBEAT_TIMEOUT_MS = 60000`):**
  - While connected, Windows transmits an active state sync every 3 seconds.
  - If no heartbeat is received for 60 seconds while connected, `displayedColor` switches to Yellow.
- **Flicker-Free LED Updating:**
  - `applyColor(c)` checks `if (displayedColor == c) return;`. It suppresses redundant GPIO toggles, flash writes, and serial logs during steady-state heartbeats.

---

## 💻 Windows Companion Subsystems (`windows/`)

### Architecture
- **Language / Runtime:** Go 1.22+ (`main.go`).
- **Bluetooth Stack:** `tinygo.org/x/bluetooth` with local Windows WinRT async timeout patch (`windows/third_party/bluetooth/adapter_windows.go`).
- **State Management (`windows/state/manager.go`):**
  - Thread-safe manager handling `Color`, `Mode` (`AUTO` vs `MANUAL`), `ZoomMeeting`, `SessionLocked`, and `Connected`.
  - **Color Evaluation Priority (Auto Mode):**
    `Disconnected (OFF)` > `Zoom Meeting (RED)` > `Workstation Locked (YELLOW)` > `Available (GREEN)`.
- **Zoom Meeting Detector (`windows/zoom/detector.go`):**
  - Native Win32 window scanner (checks `ZPContentViewWnd`, `ZPFloatVideoWndClass`, and meeting titles). Requires no Zoom API tokens.
- **Session Lock Detector (`windows/session/lock_windows.go`):**
  - Listens for `WM_WTSSESSION_CHANGE` (`WTS_SESSION_LOCK` / `WTS_SESSION_UNLOCK`).
- **Global Hotkeys (`windows/hotkey/hotkey.go`):**
  - `Ctrl + Shift + G`: Green (Available)
  - `Ctrl + Shift + Y`: Yellow (Away)
  - `Ctrl + Shift + R`: Red (In Meeting)
  - `Ctrl + Shift + A`: Auto Mode (Resume Zoom/Lock sync)
- **Web Dashboard & REST API (`windows/server/server.go`):**
  - Serves status dashboard and pairing UI at `http://localhost:<dynamic_port>` (saved in `%APPDATA%\SignalLight\config.json`).
  - Endpoints: `GET /api/status`, `POST /api/set?color=...`, `GET /api/ble/scan`, `POST /api/ble/pair`, `POST /api/ble/unpair`.

---

## 🛠️ Build & Development Guidelines for AI

### 1. Windows App Execution (Smart App Control)
- Windows 11 Smart App Control (SAC) blocks newly compiled unsigned `.exe` binaries if executed directly.
- **Always run via source or runner script during development:**
  ```powershell
  cd C:\Users\jdtom\dev\SignalLight\windows
  .\run.bat
  # Or: go run .
  ```
- Do not attempt to generate or install root certificates without user consent.

### 2. Modifying Arduino Code
- Arduino sketch location: [`arduino/SignalLight/SignalLight.ino`](file:///C:/Users/jdtom/dev/SignalLight/arduino/SignalLight/SignalLight.ino).
- **Never call `BLE.connected()` repeatedly inside `loop()`:** Polling `BLE.connected()` on ESP32-S3 calls `HCI.poll()` and can intermittently return false during packet parsing, causing false disconnects. Rely on `blePeripheralDisconnectHandler`.
- **Always preserve `activeColor` across disconnects:** Never overwrite `activeColor` or `active_color` in NVS when entering fallback Yellow mode.

### 3. Modifying Windows BLE Client
- BLE client location: [`windows/ble/client.go`](file:///C:/Users/jdtom/dev/SignalLight/windows/ble/client.go).
- Default `lastSentColor` must remain `'G'` (`GREEN`), never `'Y'`.
- WinRT write operations: Use `writeControl` which attempts acknowledged `Write` first, with graceful fallback to `WriteWithoutResponse`.
- Always verify COM apartment initialization (`runtime.LockOSThread()`, `ole.RoInitialize(1)`) on any new goroutines interacting with WinRT.
