/*
 * SignalLight - Arduino Nano ESP32 Firmware
 * 
 * Hardware:
 *   - Board: Arduino Nano ESP32 (ESP32-S3)
 *   - Pin D2: RED LED (In Meeting / Busy)
 *   - Pin D3: YELLOW LED (Away / Default)
 *   - Pin D4: GREEN LED (Available / Free)
 * 
 * Identity & Security:
 *   - Unpaired: Advertises as "SignalLight-[Last 4 MAC]" (e.g. SignalLight-69F5).
 *               Yellow LED pulses to indicate awaiting pairing.
 *   - Paired:   Advertises with custom name (e.g. "Office Desk").
 *               Requires "AUTH:<secret>" handshake within 4s of connection.
 *   - Reset:    Press the physical Reset button TWICE within 3s to factory reset.
 *               Or write "UNPAIR" from the app, or type "FACTORY_RESET" in Serial.
 */

#include <ArduinoBLE.h>
#include <Preferences.h>
#include <esp_mac.h>

// External LED Pin definitions
const int PIN_RED    = D2;
const int PIN_YELLOW = D3;
const int PIN_GREEN  = D4;

// On-board RGB LED is defined by Arduino Nano ESP32 core (LED_RED, LED_GREEN, LED_BLUE: Active-LOW)

// BLE UUIDs
const char* BLE_SERVICE_UUID = "19B10000-E8F2-537E-4F6C-D104768A1214";
const char* BLE_CHAR_UUID    = "19B10001-E8F2-537E-4F6C-D104768A1214";
const char* BLE_AUTH_UUID    = "19B10002-E8F2-537E-4F6C-D104768A1214";

BLEService lightService(BLE_SERVICE_UUID);
BLEByteCharacteristic lightCharacteristic(BLE_CHAR_UUID, BLERead | BLEWrite | BLEWriteWithoutResponse | BLENotify);
BLEStringCharacteristic authCharacteristic(BLE_AUTH_UUID, BLERead | BLEWrite | BLENotify, 64);

// NVS Persistent Storage
Preferences prefs;
bool isPaired = false;
String deviceName = "";
String sharedSecret = "";

// Security & Connection State
BLEDevice activeCentral;
bool isCentralConnected = false;
bool isAuthenticated = false;
unsigned long connectTime = 0;
const unsigned long AUTH_TIMEOUT_MS = 4000;

// Watchdog & Disconnect Timers
const unsigned long HEARTBEAT_TIMEOUT_MS = 60000; // 60s timeout while connected
const unsigned long DISCONNECT_GRACE_MS   = 45000; // 45s grace period on disconnect before turning Yellow
const unsigned long PULSE_CYCLE_MS  = 1000; // Period of the unpaired "awaiting pairing" pulse
const unsigned long PULSE_ON_MS     = 700;  // How much of each cycle the pulse stays lit
unsigned long lastHeartbeatTime = 0;
char activeColor = 'G';     // The desired operational color (survives disconnects)
char displayedColor = ' ';  // The actual color currently illuminated on the LEDs

// Double-press reset detection via flash (survives power cycles and EN pin reset)
unsigned long bootTime = 0;
bool resetArmed = false;

// Control the onboard RGB LED (active-LOW logic) and onboard Yellow LED (LED_BUILTIN)
void setOnboardRGB(bool redOn, bool greenOn, bool blueOn) {
  digitalWrite(LED_RED, redOn ? LOW : HIGH);
  digitalWrite(LED_GREEN, greenOn ? LOW : HIGH);
  digitalWrite(LED_BLUE, blueOn ? LOW : HIGH);
  digitalWrite(LED_BUILTIN, (redOn && greenOn && !blueOn) ? HIGH : LOW);
}

void flashLED(int pin, int times, int delayMs) {
  for (int i = 0; i < times; i++) {
    digitalWrite(pin, HIGH);
    if (pin == PIN_RED) {
      setOnboardRGB(true, false, false);
    } else if (pin == PIN_GREEN) {
      setOnboardRGB(false, true, false);
    }
    delay(delayMs);

    digitalWrite(pin, LOW);
    setOnboardRGB(false, false, false);
    delay(delayMs);
  }
}

void applyColor(char c) {
  if (displayedColor == c) {
    return; // Already showing this color; prevent flicker, flash wear, and log spam
  }
  displayedColor = c;
  
  digitalWrite(PIN_RED, LOW);
  digitalWrite(PIN_YELLOW, LOW);
  digitalWrite(PIN_GREEN, LOW);

  switch (c) {
    case 'R':
      digitalWrite(PIN_RED, HIGH);
      setOnboardRGB(true, false, false); // Onboard RED
      Serial.println("[State] -> RED (In Meeting)");
      break;
    case 'G':
      digitalWrite(PIN_GREEN, HIGH);
      setOnboardRGB(false, true, false); // Onboard GREEN
      Serial.println("[State] -> GREEN (Available)");
      break;
    case 'Y':
    default:
      digitalWrite(PIN_YELLOW, HIGH);
      setOnboardRGB(true, true, false);  // Onboard YELLOW (Red + Green)
      Serial.println("[State] -> YELLOW (Away / Disconnected)");
      displayedColor = 'Y';
      break;
    case '0':
      setOnboardRGB(false, false, false); // All OFF
      Serial.println("[State] -> ALL OFF");
      break;
  }

  lightCharacteristic.writeValue((byte)displayedColor);
}

void setActiveColor(char c) {
  c = toupper(c);
  if (c != 'R' && c != 'G' && c != 'Y' && c != '0') {
    return;
  }
  activeColor = c;

  // Persist active color so reboots/brownouts hold the same state seamlessly
  if (isPaired) {
    prefs.begin("signallight", false);
    if (prefs.getChar("active_color", ' ') != activeColor) {
      prefs.putChar("active_color", activeColor);
    }
    prefs.end();
  }

  applyColor(activeColor);
}

void factoryReset() {
  Serial.println("[Reset] Wiping settings from flash...");
  prefs.begin("signallight", false);
  prefs.clear();
  prefs.end();

  // Flash RED 3 times
  digitalWrite(PIN_YELLOW, LOW);
  digitalWrite(PIN_GREEN, LOW);
  flashLED(PIN_RED, 3, 150);

  Serial.println("[Reset] Factory reset complete. Deinitializing BLE and rebooting...");
  delay(300);
  BLE.end();
  delay(100);
  ESP.restart();
}

void processControlCommand(char cmd) {
  if (isPaired && !isAuthenticated) {
    Serial.println("[Security] Control command ignored: not authenticated.");
    return;
  }

  cmd = toupper(cmd);
  lastHeartbeatTime = millis();

  switch (cmd) {
    case 'R':
    case 'Y':
    case 'G':
    case '0':
      // Explicit log of the raw command + its source: helps distinguish an
      // intentional color change (hotkey/dashboard/heartbeat resync) from the
      // watchdog's own distinct "[Watchdog] ... Reverting to YELLOW" log lines below.
      Serial.print("[Control] Received explicit command from host: ");
      Serial.println(cmd);
      setActiveColor(cmd);
      break;
    case 'P':
      // Heartbeat ping: If previously fallen back to Yellow due to disconnect/watchdog, restore activeColor
      if (displayedColor != activeColor) {
        applyColor(activeColor);
      }
      break;
    case '?':
      Serial.print("STATUS:");
      Serial.println(displayedColor);
      break;
    default:
      break;
  }
}

// constantTimeEquals compares two secrets without an early exit on the first mismatched
// byte, so a BLE-adjacent attacker can't use response timing to narrow down the secret
// one byte at a time. (String::equals() is not guaranteed to have this property.)
bool constantTimeEquals(const String &a, const String &b) {
  size_t lenA = a.length();
  size_t lenB = b.length();
  size_t maxLen = lenA > lenB ? lenA : lenB;
  uint8_t diff = (lenA != lenB) ? 1 : 0;
  for (size_t i = 0; i < maxLen; i++) {
    char ca = (i < lenA) ? a[i] : 0;
    char cb = (i < lenB) ? b[i] : 0;
    diff |= (uint8_t)(ca ^ cb);
  }
  return diff == 0;
}

void processAuthMessage(String msg) {
  msg.trim();
  Serial.print("[Auth] Received message: ");
  Serial.println(msg.startsWith("AUTH:") ? "AUTH:****" : (msg.startsWith("PAIR:") ? "PAIR:[name]:****" : msg));

  // 1. Initial Pairing: "PAIR:<name>:<secret>"
  if (msg.startsWith("PAIR:")) {
    // PAIR: is only valid in factory-unpaired mode. Previously this only blocked
    // re-pairing while unauthenticated, which meant an already-authenticated central
    // could silently overwrite the stored name/secret with no confirmation step.
    // Re-pairing now always requires an explicit UNPAIR (factory reset) first.
    if (isPaired) {
      authCharacteristic.writeValue("ERR:ALREADY_PAIRED");
      return;
    }

    int firstColon = msg.indexOf(':');
    int secondColon = msg.indexOf(':', firstColon + 1);
    if (secondColon == -1) {
      authCharacteristic.writeValue("ERR:BAD_FORMAT");
      return;
    }

    String newName = msg.substring(firstColon + 1, secondColon);
    String newSecret = msg.substring(secondColon + 1);
    newName.trim();
    newSecret.trim();

    if (newName.length() == 0 || newSecret.length() == 0) {
      authCharacteristic.writeValue("ERR:EMPTY_FIELDS");
      return;
    }

    // Save configuration to NVS
    prefs.begin("signallight", false);
    prefs.putBool("paired", true);
    prefs.putString("name", newName);
    prefs.putString("secret", newSecret);
    prefs.putBool("rst_armed", false);
    prefs.putChar("active_color", 'G');
    prefs.end();

    isPaired = true;
    deviceName = newName;
    sharedSecret = newSecret;
    isAuthenticated = true;
    activeColor = 'G';

    authCharacteristic.writeValue("PAIR_OK");
    Serial.print("[Auth] Paired as '");
    Serial.print(newName);
    Serial.println("'. Connection maintained.");

    digitalWrite(PIN_YELLOW, LOW);
    flashLED(PIN_GREEN, 3, 150);
    applyColor(activeColor);
    return;
  }

  // 2. Authentication: "AUTH:<secret>"
  if (msg.startsWith("AUTH:")) {
    if (!isPaired) {
      Serial.println("[Auth] Rejected AUTH: device is in factory unpaired mode.");
      authCharacteristic.writeValue("ERR:NOT_PAIRED");
      return;
    }

    String incomingSecret = msg.substring(5);
    incomingSecret.trim();

    if (constantTimeEquals(incomingSecret, sharedSecret)) {
      isAuthenticated = true;
      authCharacteristic.writeValue("AUTH_OK");
      Serial.println("[Auth] Authentication SUCCESS.");
      applyColor(activeColor);
    } else {
      authCharacteristic.writeValue("AUTH_FAIL");
      Serial.println("[Auth] Authentication FAILED! Disconnecting central.");
      delay(150);
      if (activeCentral && activeCentral.connected()) {
        activeCentral.disconnect();
      }
    }
    return;
  }

  // 3. Unpair / Factory Reset: "UNPAIR"
  if (msg.equals("UNPAIR")) {
    if (isPaired && !isAuthenticated) {
      authCharacteristic.writeValue("ERR:NOT_AUTHENTICATED");
      return;
    }
    authCharacteristic.writeValue("UNPAIR_OK");
    delay(200);
    factoryReset();
    return;
  }

  // 4. Status Query
  if (msg.equals("STATUS?")) {
    authCharacteristic.writeValue(isPaired ? "STATUS:PAIRED" : "STATUS:UNPAIRED");
    return;
  }
}

volatile bool needAdvertise = false;
unsigned long disconnectTime = 0;

void blePeripheralConnectHandler(BLEDevice central) {
  activeCentral = central;
  isCentralConnected = true;
  connectTime = millis();
  lastHeartbeatTime = millis();
  isAuthenticated = !isPaired; // In unpaired mode, automatically allow connection

  Serial.print("[BLE] Central connected: ");
  Serial.println(central.address());

  if (isPaired) {
    authCharacteristic.writeValue("STATUS:PAIRED");
    Serial.println("[BLE] Device is PAIRED. Waiting for AUTH:<secret> within 4s...");
  } else {
    authCharacteristic.writeValue("STATUS:UNPAIRED");
    Serial.println("[BLE] Device is UNPAIRED. Ready for PAIR:<name>:<secret>");
  }
}

void blePeripheralDisconnectHandler(BLEDevice central) {
  Serial.print("[BLE] Central disconnected: ");
  Serial.println(central.address());
  isCentralConnected = false;
  isAuthenticated = false;
  needAdvertise = true;
  disconnectTime = millis();
}

void setup() {
  pinMode(PIN_RED, OUTPUT);
  pinMode(PIN_YELLOW, OUTPUT);
  pinMode(PIN_GREEN, OUTPUT);

  pinMode(LED_RED, OUTPUT);
  pinMode(LED_GREEN, OUTPUT);
  pinMode(LED_BLUE, OUTPUT);
  pinMode(LED_BUILTIN, OUTPUT);

  Serial.begin(115200);
  delay(300);
  Serial.println("\n=========================================");
  Serial.println("SignalLight Controller (Nano ESP32)");

  // 1. Double-Press Reset Detection via Flash
  prefs.begin("signallight", false);
  bool wasArmed = prefs.getBool("rst_armed", false);
  if (wasArmed) {
    // Reset occurred while rst_armed was true (user pressed Reset button twice within 4s)
    Serial.println("[Reset] DOUBLE-PRESS DETECTED! Resetting to factory defaults...");
    prefs.clear();
    prefs.putBool("rst_armed", false);
    prefs.end();

    // Flash RED 3 times
    digitalWrite(PIN_YELLOW, LOW);
    digitalWrite(PIN_GREEN, LOW);
    flashLED(PIN_RED, 3, 150);

    Serial.println("[Reset] Factory reset complete. Continuing boot in unpaired mode...");
    bootTime = millis();
    resetArmed = false;
  } else {
    prefs.putBool("rst_armed", true);
    prefs.end();
    bootTime = millis();
    resetArmed = true;
  }

  // 2. Read Factory Hardware MAC Address directly from eFuse
  uint8_t baseMac[6];
  esp_read_mac(baseMac, ESP_MAC_BT);
  char macSuffix[5];
  snprintf(macSuffix, sizeof(macSuffix), "%02X%02X", baseMac[4], baseMac[5]);
  String defaultName = "SignalLight-" + String(macSuffix);

  // 3. Load Saved Settings from Flash
  prefs.begin("signallight", false);
  isPaired = prefs.getBool("paired", false);
  deviceName = prefs.getString("name", "");
  sharedSecret = prefs.getString("secret", "");
  char savedColor = prefs.getChar("active_color", 'G');
  prefs.end();

  // If already paired, restore the active color immediately so reboots don't cause a yellow glitch
  if (isPaired && (savedColor == 'G' || savedColor == 'R' || savedColor == 'Y' || savedColor == '0')) {
    activeColor = savedColor;
    applyColor(activeColor);
  } else {
    applyColor('Y');
  }

  String activeName = defaultName;
  if (isPaired && deviceName.length() > 0) {
    activeName = deviceName;
  }

  Serial.print("[Config] Status: ");
  Serial.println(isPaired ? "PAIRED" : "UNPAIRED");
  Serial.print("[Config] Advertising Name: ");
  Serial.println(activeName);

  // 4. Initialize BLE (Only ONCE)
  if (!BLE.begin()) {
    Serial.println("ERR: BLE.begin() failed!");
  } else {
    BLE.setLocalName(activeName.c_str());
    BLE.setDeviceName(activeName.c_str());
    BLE.setAdvertisedService(lightService);

    lightService.addCharacteristic(lightCharacteristic);
    lightService.addCharacteristic(authCharacteristic);
    BLE.addService(lightService);

    lightCharacteristic.writeValue((byte)displayedColor);
    authCharacteristic.writeValue(isPaired ? "STATUS:PAIRED" : "STATUS:UNPAIRED");

    BLE.setEventHandler(BLEConnected, blePeripheralConnectHandler);
    BLE.setEventHandler(BLEDisconnected, blePeripheralDisconnectHandler);

    BLE.advertise();
    Serial.println("[BLE] Advertising active.");
  }

  Serial.println("Ready!");
  Serial.println("=========================================");
}

void loop() {
  unsigned long now = millis();

  // 1. Disarm double-press reset detection after 4 seconds of stable uptime
  if (resetArmed && (now - bootTime > 4000)) {
    prefs.begin("signallight", false);
    prefs.putBool("rst_armed", false);
    prefs.end();
    resetArmed = false;
    Serial.println("[Reset] Double-press window expired. Reset disarmed.");
  }

  // 2. Poll BLE stack
  BLE.poll();

  // 3. Restart advertising safely after disconnect (outside the callback)
  if (needAdvertise && (now - disconnectTime >= 200)) {
    needAdvertise = false;
    Serial.println("[BLE] Resuming advertising...");
    BLE.advertise();
  }

  // 4. Security Timeout Check (disconnect central if it didn't authenticate in 4s)
  if (isCentralConnected && isPaired && !isAuthenticated) {
    if (now - connectTime > AUTH_TIMEOUT_MS) {
      Serial.println("[Security] Auth timeout exceeded! Disconnecting central.");
      if (activeCentral && activeCentral.connected()) {
        activeCentral.disconnect();
      }
    }
  }

  // 5. Handle Auth Characteristic
  if (authCharacteristic.written()) {
    String msg = authCharacteristic.value();
    processAuthMessage(msg);
  }

  // 6. Handle Control Characteristic
  if (lightCharacteristic.written()) {
    byte val = lightCharacteristic.value();
    processControlCommand((char)val);
  }

  // 7. Handle USB Serial Commands
  if (Serial.available() > 0) {
    String input = Serial.readStringUntil('\n');
    input.trim();
    if (input.equalsIgnoreCase("FACTORY_RESET") || input.equalsIgnoreCase("UNPAIR")) {
      factoryReset();
    } else if (input.length() > 0) {
      char firstChar = toupper(input.charAt(0));
      processControlCommand(firstChar);
    }
  }

  // 8. Visual Pulse when Unpaired and Awaiting Connection
  if (!isPaired && !isCentralConnected) {
    unsigned long cycle = now % PULSE_CYCLE_MS;
    if (cycle < PULSE_ON_MS) {
      digitalWrite(PIN_YELLOW, HIGH);
      setOnboardRGB(true, true, false); // Onboard Yellow (Red + Green)
    } else {
      digitalWrite(PIN_YELLOW, LOW);
      setOnboardRGB(false, false, false); // Onboard OFF
    }
  }

  // 9. Failsafe Watchdog & Disconnect Timeout
  if (isPaired) {
    if (isCentralConnected) {
      // While connected: timeout if no heartbeat received for 60s
      if (now - lastHeartbeatTime > HEARTBEAT_TIMEOUT_MS) {
        if (displayedColor != 'Y') {
          Serial.println("[Watchdog] Heartbeat timeout while connected. Reverting to YELLOW.");
          applyColor('Y');
        }
      }
    } else {
      // While disconnected: hold previous color for DISCONNECT_GRACE_MS (45s), then turn Yellow
      if (now - disconnectTime > DISCONNECT_GRACE_MS) {
        if (displayedColor != 'Y') {
          Serial.println("[Watchdog] Disconnect grace period expired. Reverting to YELLOW.");
          applyColor('Y');
        }
      }
    }
  }
}
