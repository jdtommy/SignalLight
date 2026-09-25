# SignalLight 🚦

An intelligent status light project using the **Arduino Nano ESP32 (ESP32-S3)** and a **Windows Go (Golang)** desktop companion.

The light indicates your real-time availability:
- 🔴 **RED** (`D2`): In a meeting / Busy (automatically triggered by Zoom or hotkey)
- 🟡 **YELLOW** (`D3`): Not at desk / Away (**Default** on boot and failsafe disconnect)
- 🟢 **GREEN** (`D4`): Free / Available to talk

---

## ⚡ Hardware & Electrical Guide

### ⚠️ Critical Voltage & Wiring Safety Note
> [!WARNING]
> **ESP32-S3 logic levels are strictly 3.3V.**
> Although the Arduino Nano ESP32 board is powered by 5V (via USB-C or `VBUS`), the microcontroller GPIO pins (D2, D3, D4) are **NOT 5V tolerant** (maximum 3.6V).
> 
> If you connect a 10k pull-up resistor from **D3** to **5V**, you will place 5V directly onto an ESP32 GPIO pin. While a 10k resistor limits current, it violates the chip's absolute maximum ratings.
> 
> **Recommended Fix:**
> Connect your 10k pull-up resistor to the **`3V3`** pin on the Arduino header instead of the `5V` pin. This safely pulls D3 HIGH to 3.3V on power-up.

### 💡 Driving 12V LEDs from 3.3V Logic
The ESP32 GPIO pins output 3.3V logic and cannot directly drive 12V LEDs. You should use **Logic-Level N-Channel MOSFETs** (e.g., `IRLZ44N`, `2N7000`, `AO3400`) or a transistor/relay driver module:

```
                      +12V Supply
                           |
                           +--> [ + 12V LED Strip / Lamp ]
                                     |
                                     | (-) Cathode
                                   [ D ]  (Drain)
Arduino Pin (D2/D3/D4) --[100Ω]--> [ G ]  N-Channel Logic MOSFET
                                   [ S ]  (Source)
                                     |
                                    GND
                                     |
             Common GND <------------+------------> Arduino GND
```

#### Yellow Default Circuit:
To keep Yellow ON when the Arduino is disconnected or powered off:
- Place a **10kΩ pull-up resistor** between the **Gate (G)** of the Yellow MOSFET and the **3.3V** rail (or 5V rail if isolated from the GPIO pin by a resistor).
- When the Arduino boots, the firmware immediately drives D3 HIGH and D2/D4 LOW.

### 🖨️ Custom Driver PCB
[`jdtommy-status-indicator.edif`](file:///C:/Users/jdtom/dev/SignalLight/jdtommy-status-indicator.edif) (netlist), [`status-indicator-dxf/`](file:///C:/Users/jdtom/dev/SignalLight/status-indicator-dxf) / [`status-indicator-svg/`](file:///C:/Users/jdtom/dev/SignalLight/status-indicator-svg) (plot previews), and [`jdtommy-status-indicator-Gerbers-Version1ef73905/`](file:///C:/Users/jdtom/dev/SignalLight/jdtommy-status-indicator-Gerbers-Version1ef73905) (fab package) are a small 2-layer PCB export (from [Flux](https://flux.ai)) that replaces the breadboard wiring above with a real board. It mounts an Arduino Nano ESP32 and drives the three LED channels through three `2N3904`-family NPN transistors (instead of the MOSFETs described above — a valid alternative for a modest LED current draw) as low-side switches, with the same Yellow-defaults-on pull-up resistor design:

| Connector | Pin | Signal |
|-----------|-----|--------|
| **CN1** (2-pin, power in) | 1 | GND |
| | 2 | +12V |
| **CN2** (4-pin, LED out) | 1 | +12V (common anode) |
| | 2 | Red cathode (via Q4, driven by D2, R2 = 1kΩ base resistor) |
| | 3 | Yellow cathode (via Q5, driven by D3, R3 = 1kΩ base resistor, **R1 = 10kΩ pull-up to 3.3V**) |
| | 4 | Green cathode (via Q6, driven by D4, R4 = 1kΩ base resistor) |

Board outline is roughly **19mm × 45mm**.

> [!NOTE]
> **Bare PCB fabrication: ready to order.** `jdtommy-status-indicator-Gerbers-Version1ef73905/` has properly formatted Gerber X2 files for all layers, a real Excellon drill file, and an IPC-D-356 bare-board test netlist — this is a standard fab package any house (JLCPCB, PCBWay, OSH Park, etc.) should accept directly. (The DXF/SVG folders are just plot previews from an earlier export and aren't needed for ordering.)
>
> **Turnkey SMD assembly: not ready — fix the BOM first.** Every vendor BOM CSV in `BOM/` groups R1–R4 into a single line item labeled "1kΩ" (Flux's exporter grouped them by shared footprint and lost the distinct value). The correct values — confirmed in `pick_and_place.csv` — are **R1 = 10kΩ**, **R2/R3/R4 = 1kΩ**. If you submit a BOM as-is, R1 (the Yellow failsafe pull-up) would get placed as 1kΩ instead of 10kΩ: Yellow would still default on, just with ~10x more continuous current through that pull-up than intended. Split R1 into its own line before ordering assembly.
>
> Also note: **U2 (the Arduino Nano ESP32) can't be placed by any SMT line** — it's a whole dev board, not a stocked part, so it needs to be hand-soldered/socketed on regardless of which assembly path you use. The two JST connectors (CN1/CN2) do have real LCSC part numbers (`C158012`, `C144395`) if you want an assembly house to place those.

---

## 📁 Project Structure

```
SignalLight/
├── arduino/
│   ├── SignalLight/
│   │   └── SignalLight.ino      # Arduino C++ sketch (BLE + Serial + Failsafe)
│   └── micropython/
│       └── main.py              # MicroPython equivalent (deprecated, see below)
├── windows/
│   ├── ble/                     # Bluetooth Low Energy (BLE) client
│   ├── hotkey/                  # Global Windows hotkeys (Ctrl+Shift+R/Y/G/A)
│   ├── serial/                  # USB Serial communication fallback
│   ├── server/                  # Web dashboard UI, REST API & Zoom webhook
│   ├── state/                   # State machine (Auto vs Manual, Red/Yellow/Green)
│   ├── zoom/                    # Zero-config Zoom meeting window detector
│   ├── go.mod / go.sum
│   ├── main.go                  # Windows application entry point
│   └── signallight.exe          # Compiled standalone Windows executable
├── jdtommy-status-indicator.edif              # Driver PCB netlist (see Custom Driver PCB above)
├── status-indicator-dxf/                      # Driver PCB plot previews (DXF, not needed for ordering)
├── status-indicator-svg/                      # Driver PCB plot previews (SVG, not needed for ordering)
├── jdtommy-status-indicator-Gerbers-Version1ef73905/   # Driver PCB fab package: Gerbers, drill file, BOMs, pick-and-place (see above)
└── README.md
```

---

## 🚀 Part 1: Arduino Firmware Setup

### Option A: Arduino IDE (Recommended)
1. Open the **Arduino IDE**.
2. Install the **Arduino ESP32 Boards** package:
   - Go to `Tools -> Board -> Boards Manager...`
   - Search for **Arduino Nano ESP32** and install.
3. Install the **ArduinoBLE** library:
   - Go to `Sketch -> Include Library -> Manage Libraries...`
   - Search for **ArduinoBLE** and click **Install**.
4. Open [`arduino/SignalLight/SignalLight.ino`](file:///C:/Users/jdtom/dev/SignalLight/arduino/SignalLight/SignalLight.ino).
5. Select `Tools -> Board -> Arduino Nano ESP32` and choose your COM port.
6. Click **Upload**.

### Option B: MicroPython (⚠️ Deprecated / Not Compatible)
[`arduino/micropython/main.py`](file:///C:/Users/jdtom/dev/SignalLight/arduino/micropython/main.py) is kept only as a reference starting point. It has no Auth
characteristic and cannot complete the PAIR/AUTH handshake the Windows app requires, so
pairing from the web dashboard will not work against it. Use Option A.

---

## 🖥️ Part 2: Windows Companion App (in Go)

The Windows app is written in **Go (Golang)**. It features:
1. **Bluetooth Low Energy (BLE)**: Automatically scans for and connects to the Nano ESP32 wirelessly.
2. **Zoom Meeting Detection**: Detects active Zoom meetings automatically with **zero configuration** by monitoring Zoom meeting windows (`ZPContentViewWnd`, `ZPFloatVideoWndClass`, and meeting titles).
3. **Screen Lock Detection:** Automatically detects when you lock your Windows workstation (<kbd>Win</kbd> + <kbd>L</kbd> or idle screen timeout):
   - Automatically switches to 🟡 **YELLOW (Away)** when locked.
   - Automatically restores 🟢 **GREEN (Available)** when unlocked.
   - Priority rule: 🔴 **Zoom Meeting (RED)** always takes precedence over the lock screen.
4. **Global Hotkeys**:
   - <kbd>Ctrl</kbd> + <kbd>Shift</kbd> + <kbd>G</kbd> : Set **GREEN** (Free / Available)
   - <kbd>Ctrl</kbd> + <kbd>Shift</kbd> + <kbd>Y</kbd> : Set **YELLOW** (Away / Not at Desk)
   - <kbd>Ctrl</kbd> + <kbd>Shift</kbd> + <kbd>R</kbd> : Set **RED** (Busy / In Meeting)
   - <kbd>Ctrl</kbd> + <kbd>Shift</kbd> + <kbd>A</kbd> : Set **AUTO** (Resume syncing with Zoom & Lock)
5. **Windows System Tray Icon:** A live, color-coded icon in your taskbar notification area:
   - Dynamic icon color: 🟢 Green, 🟡 Yellow, or 🔴 Red matching current status.
   - Click to open quick menu:
     - View current status & mode
     - Force Green (Available)
     - Force Yellow (Away)
     - Force Red (Busy)
     - Enable Auto Mode (Sync with Zoom & Lock)
     - Open Web Dashboard
     - Exit application
6. **Interactive Web Dashboard**: Automatically assigns an open local port (e.g. `http://localhost:9439`), displayed on startup and saved to config so bookmarks stay valid. Visit the dashboard to pair devices, see real-time status, and control the light from any browser.
7. **Fail-safe Heartbeat**: Continuously pings the Arduino. If your PC goes to sleep or disconnects, the Arduino automatically defaults back to **YELLOW** after 15 seconds.

### Running the App

#### Method 1: Running from Source (Recommended)
This bypasses Windows 11 Smart App Control policies without requiring code-signing certificates:
```powershell
cd C:\Users\jdtom\dev\SignalLight\windows
go run .
```
Or simply double-click [`windows/run.bat`](file:///C:/Users/jdtom/dev/SignalLight/windows/run.bat) or run [`windows/run.ps1`](file:///C:/Users/jdtom/dev/SignalLight/windows/run.ps1).

#### Method 2: Using the Pre-compiled Binary
Download the signed `signallight.exe` from the [latest GitHub Release](https://github.com/jdtommy/SignalLight/releases/latest), or build your own from source:
```powershell
cd C:\Users\jdtom\dev\SignalLight\windows
.\signallight.exe
```
> [!NOTE]
> Binaries attached to GitHub Releases are signed with a publicly-trusted Authenticode certificate (via Azure Trusted Signing), so Windows 11 **Smart App Control (SAC)** should allow them without a warning. A binary you compile yourself locally (`go build`/`go run`) is **not** signed — signing requires a live call to Azure with valid credentials — so SAC may still block a self-built `.exe`. If it does, run via `go run .` or download the signed release instead.

#### Command Line Options
| Flag | Default | Description |
|------|---------|-------------|
| `-ble` | `true` | Connect to Arduino over Bluetooth Low Energy |
| `-device` | `""` | BLE advertised device name (overrides config) |
| `-serial` | `""` | Optional USB COM port (e.g. `COM3` or `auto`) if using USB cable |
| `-port` | `""` | Web dashboard port (default: auto-allocated open port, e.g. 9120-9900, saved in config.json) |
| `-interval` | `1s` | Zoom meeting polling interval |

---

## 📋 Future Roadmap & TODO
- [x] **Binary Code Signing**: `signallight.exe` attached to GitHub Releases is signed via [Azure Trusted Signing](https://azure.microsoft.com/en-us/products/artifact-signing) (Public Trust, individual developer), so it should run under Smart App Control without a warning.
- [ ] **Automate release signing**: signing is currently a manual step (`sign code artifact-signing`, requires the maintainer's own `az login` session) run before uploading a release asset — move this into a CI/release pipeline so it isn't a manual, single-person-dependent step.
- [ ] **MSIX Packaging**: consider MSIX packaging as an alternative/complementary distribution method.

---

## 🔗 Optional: Zoom Webhook Integration
The local desktop detector works without any setup. If you also want cloud-level presence notifications from Zoom:
1. Create a **Server-to-Server OAuth** or **Webhook Only** app in the [Zoom App Marketplace](https://marketplace.zoom.us/).
2. Set the Webhook endpoint URL to: `https://<your-public-url>/webhook/zoom`.
3. Subscribe to events: `meeting.started`, `meeting.ended`, `user.presence_status_updated`.
4. Start `signallight.exe` with your secret token:
   ```powershell
   .\signallight.exe -zoom-secret="YOUR_ZOOM_SECRET_TOKEN"
   ```

---

## 📄 License
MIT — see [LICENSE](LICENSE).

## 🔒 Privacy & Code Signing
- [Privacy Policy](PRIVACY.md) — SignalLight collects no data; everything runs locally.
- [Code Signing Policy](CODE_SIGNING_POLICY.md) — official repository, signed artifacts, and release process.
