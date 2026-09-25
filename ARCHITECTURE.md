# SignalLight — Architecture & Technical Reference

This document details the underlying software architecture, communication protocols, state machines, and concurrency patterns used in **SignalLight**.

---

## 1. Bluetooth Low Energy (BLE) Protocol

The Arduino Nano ESP32 operates as a **GATT Server (Peripheral)**, and the Windows Go desktop companion operates as a **GATT Client (Central)**.

### GATT Service & Characteristics

```
Service: 19B10000-E8F2-537E-4F6C-D104768A1214
 ├── Characteristic 19B10001 (Control) [Read | Write | WriteWithoutResponse | Notify] (1 byte)
 └── Characteristic 19B10002 (Auth)    [Read | Write | Notify] (UTF-8 String, max 64 bytes)
```

#### Control Characteristic (`19B10001`)
Used for state synchronization, manual commands, and heartbeat pings:
- `'R'` (0x52): In Meeting / Busy.
- `'Y'` (0x59): Away / Workstation Locked.
- `'G'` (0x47): Free / Available.
- `'0'` (0x30): All Off.
- `'P'` (0x50): Heartbeat Ping.
- `'?'` (0x3F): Status Query (returns current color via Serial / BLE read).

#### Auth Characteristic (`19B10002`)
Used for discovery, secure pairing handshakes, and unpairing:
- `PAIR:<FriendlyName>:<SharedSecret>`: Initial pairing command when device is in factory unpaired mode. Returns `PAIR_OK` or error (`ERR:ALREADY_PAIRED`, `ERR:BAD_FORMAT`).
- `AUTH:<SharedSecret>`: Authentication challenge response sent by Central upon each connection. Returns `AUTH_OK` or `AUTH_FAIL`.
- `UNPAIR`: Factory resets device credentials and reboots into unpaired mode. Returns `UNPAIR_OK`.
- `STATUS?`: Queries device mode. Returns `STATUS:PAIRED` or `STATUS:UNPAIRED`.

---

## 2. Interaction Sequence Diagrams

### Initial Pairing Sequence
```
Central (Windows)                            Peripheral (Arduino Nano ESP32)
       │                                                    │
       │  Scan nearby BLE devices                           │
       │───────────────────────────────────────────────────>│
       │  Advertising: "SignalLight-69F5" (Yellow pulse)    │
       │<───────────────────────────────────────────────────│
       │                                                    │
       │  Connect                                           │
       │───────────────────────────────────────────────────>│
       │  Write (Auth): "PAIR:Office Desk:MySecret123"      │
       │───────────────────────────────────────────────────>│
       │                                                    │ Saves to NVS flash:
       │                                                    │ paired=true, name="Office Desk",
       │                                                    │ secret="MySecret123", active_color='G'
       │  Notify (Auth): "PAIR_OK"                          │
       │<───────────────────────────────────────────────────│
       │                                                    │
       │  Write (Control): 'G'                              │
       │───────────────────────────────────────────────────>│
       │                                                    │ Physical Green LED illuminated
```

---

### Reconnection & State Sync Sequence
```
Central (Windows)                            Peripheral (Arduino Nano ESP32)
       │                                                    │
       │  Connect to "Office Desk" (E8:F6:0A:BE:69:F5)      │
       │───────────────────────────────────────────────────>│
       │                                                    │ isCentralConnected = true
       │  Write (Auth): "AUTH:MySecret123"                  │ isAuthenticated = false (starts 4s timer)
       │───────────────────────────────────────────────────>│
       │                                                    │ Verifies secret against NVS flash
       │                                                    │ isAuthenticated = true
       │                                                    │ applyColor(activeColor) -> Instant Green!
       │  Notify (Auth): "AUTH_OK"                          │
       │<───────────────────────────────────────────────────│
       │                                                    │
       │  Write (Control): Current Desktop State (e.g. 'G') │
       │───────────────────────────────────────────────────>│
       │                                                    │ Confirms activeColor = 'G'
       │                                                    │
       │  Periodic Heartbeat every 3s                       │
       │  Write (Control): 'G'                              │
       │───────────────────────────────────────────────────>│
       │                                                    │ lastHeartbeatTime = millis()
```

---

### Disconnect Grace Period & Watchdog Handling
```
Scenario A: Brief RF Interference (2 seconds)
Central (Windows)                            Peripheral (Arduino Nano ESP32)
       │                                                    │
       │  (Link drop / renegotiation)                       │
       │- - - - - - - - - - - - - - - - - - - - - - - - - ->│
       │                                                    │ blePeripheralDisconnectHandler()
       │                                                    │ disconnectTime = millis()
       │                                                    │ GRACE PERIOD: 45s timer starts.
       │                                                    │ Light STAYS GREEN (No flicker!)
       │  Reconnects after 2s + Auth Handshake              │
       │───────────────────────────────────────────────────>│
       │                                                    │ isAuthenticated = true
       │                                                    │ Active Green maintained seamlessly!

Scenario B: PC Power Off / Prolonged Absence (> 45s)
       │  PC Powers Off                                     │
       │- - - - - - - - - - - - - - - - - - - - - - - - - ->│
       │                                                    │ 45s Disconnect Grace expires.
       │                                                    │ displayedColor switches to YELLOW.
       │                                                    │ (activeColor remains 'G' in flash)
       │                                                    │
       │  PC Boots next morning & reconnects                │
       │───────────────────────────────────────────────────>│
       │  "AUTH:MySecret123"                                │
       │───────────────────────────────────────────────────>│
       │                                                    │ Restores activeColor ('G') immediately!
```

---

## 3. Firmware State Management (`arduino/`)

### Flash (NVS) Layout (`Preferences` namespace: `signallight`)
- `paired` (`bool`): `true` if device has completed initial setup.
- `name` (`String`): Advertised device name (e.g. `"Office Desk"`).
- `secret` (`String`): Shared authorization secret.
- `active_color` (`char`): Last active user color (`'R'`, `'G'`, `'Y'`, `'0'`). Restored on boot.
- `rst_armed` (`bool`): Set `true` during first 4 seconds of boot. If another reset occurs while `true`, triggers a factory reset.

### Two-Tier Color Architecture
```
             ┌────────────────────────────────────────────────────────┐
             │                      activeColor                       │
             │           (User / Host Intended State: G/R/Y/0)        │
             └───────────┬────────────────────────────────┬───────────┘
                         │                                │
            Authenticated Handshake               45s Disconnect Timeout
                         │                                │
                         ▼                                ▼
             ┌───────────────────────┐        ┌───────────────────────┐
             │    displayedColor     │        │    displayedColor     │
             │     (Active Color)    │        │       (Yellow)        │
             └───────────────────────┘        └───────────────────────┘
```

---

## 4. Desktop Companion Architecture (`windows/`)

### Concurrency Model
The Go desktop companion coordinates several concurrent goroutines:

```
  ┌─────────────────────────────────────────────────────────────────────────┐
  │                              main() Goroutine                           │
  │                   Initializes subsystems & enters runtime               │
  └───────┬─────────────────┬──────────────────┬─────────────────┬──────────┘
          │                 │                  │                 │
          ▼                 ▼                  ▼                 ▼
   lifecycleLoop()    detectorLoop()     sessionLoop()     httpServer()
     (BLE Central)     (Win32 Zoom)      (Win32 Lock)      (REST / Web)
```

1. **`lifecycleLoop()` (BLE)**: Runs on an OS-locked thread (`runtime.LockOSThread()`) with initialized WinRT COM (`ole.RoInitialize(1)`). Continuously scans, connects, authenticates, and hands connection to `connectionLoop()`.
2. **`connectionLoop()` (BLE)**: Maintains the active link:
   - Evaluates a 3-second ticker for state synchronization and heartbeats.
   - Listens to `c.sendChan` for instantaneous priority color changes from the state manager.
   - Monitors OS link status via `device.Connected()` and abort signals from `SetConnectHandler`.
3. **`detectorLoop()` (Zoom)**: Polls every 1s using Win32 `EnumWindows` and `GetClassNameW` to detect active meeting video windows.
4. **`sessionLoop()` (Lock)**: Creates a hidden Win32 message window and registers for `WTSRegisterSessionNotification`.
5. **`httpServer()` (Web)**: Serves dashboard and REST API on a dynamic port without blocking core workers.

### State Evaluation Priority
The central `StateManager` computes colors in `AUTO` mode using strict hierarchy:
```
1. Not Connected   -> OFF
2. Zoom in Meeting -> RED
3. Screen Locked   -> YELLOW
4. Available       -> GREEN
```
Manual overrides bypass automatic evaluation until returning to `AUTO` via hotkey (<kbd>Ctrl</kbd> + <kbd>Shift</kbd> + <kbd>A</kbd>) or web dashboard.
