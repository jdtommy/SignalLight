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

---

## 📁 Project Structure

```
SignalLight/
├── arduino/
│   ├── SignalLight/
│   │   └── SignalLight.ino      # Arduino C++ sketch (BLE + Serial + Failsafe)
│   └── micropython/
│       └── main.py              # MicroPython equivalent
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

### Option B: MicroPython
If you flashed MicroPython onto the Nano ESP32:
- Copy [`arduino/micropython/main.py`](file:///C:/Users/jdtom/dev/SignalLight/arduino/micropython/main.py) to the board as `main.py`.

---

## 🖥️ Part 2: Windows Companion App (in Go)

The Windows app is written in **Go (Golang)**. It features:
1. **Bluetooth Low Energy (BLE)**: Automatically scans for and connects to the Nano ESP32 wirelessly.
2. **Zoom Meeting Detection**: Detects active Zoom meetings automatically with **zero configuration** by monitoring Zoom meeting windows (`ZPContentViewWnd`, `ZPFloatVideoWndClass`, and meeting titles).
3. **Global Hotkeys**:
   - <kbd>Ctrl</kbd> + <kbd>Shift</kbd> + <kbd>G</kbd> : Set **GREEN** (Free / Available)
   - <kbd>Ctrl</kbd> + <kbd>Shift</kbd> + <kbd>Y</kbd> : Set **YELLOW** (Away / Not at Desk)
   - <kbd>Ctrl</kbd> + <kbd>Shift</kbd> + <kbd>R</kbd> : Set **RED** (Busy / In Meeting)
   - <kbd>Ctrl</kbd> + <kbd>Shift</kbd> + <kbd>A</kbd> : Set **AUTO** (Resume syncing with Zoom)
4. **Interactive Web Dashboard**: Visit `http://localhost:8080` to see real-time status and manually click buttons from any browser on your machine.
5. **Fail-safe Heartbeat**: Continuously pings the Arduino. If your PC goes to sleep or disconnects, the Arduino automatically defaults back to **YELLOW** after 15 seconds.

### Running the App

#### Method 1: Using the Pre-compiled Binary
Double-click [`windows/signallight.exe`](file:///C:/Users/jdtom/dev/SignalLight/windows/signallight.exe) or run from PowerShell:
```powershell
cd C:\Users\jdtom\dev\SignalLight\windows
.\signallight.exe
```

#### Method 2: Running from Source
```powershell
cd C:\Users\jdtom\dev\SignalLight\windows
go run .
```

#### Command Line Options
| Flag | Default | Description |
|------|---------|-------------|
| `-ble` | `true` | Connect to Arduino over Bluetooth Low Energy |
| `-device` | `"SignalLight"` | BLE advertised device name |
| `-serial` | `""` | Optional USB COM port (e.g. `COM3` or `auto`) if using USB cable |
| `-port` | `":8080"` | Web dashboard port |
| `-interval` | `1s` | Zoom meeting polling interval |

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
