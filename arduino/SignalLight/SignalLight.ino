/*
 * SignalLight - Arduino Nano ESP32 Firmware
 * 
 * Hardware:
 *   - Board: Arduino Nano ESP32 (ESP32-S3)
 *   - Pin D2: RED LED (In Meeting / Busy)
 *   - Pin D3: YELLOW LED (Away / Default)
 *   - Pin D4: GREEN LED (Available / Free)
 *   - Pin D5: Unpair button (momentary, to GND, internal pull-up)
 *
 * Identity & Security:
 *   - Unpaired: Advertises as "SignalLight-[Last 4 MAC]" (e.g. SignalLight-69F5).
 *               Yellow LED pulses to indicate awaiting pairing.
 *   - Paired:   Advertises with custom name (e.g. "Office Desk").
 *               Requires "AUTH:<secret>" handshake within 4s of connection.
 *   - Reset:    Hold the D5 unpair button for 10s (light shows solid red while held,
 *               then flashes red 3 times and factory resets). Releasing early cancels.
 *               Or write "UNPAIR" from the app, or type "FACTORY_RESET" in Serial.
 */

#include <ArduinoBLE.h>
#include <Preferences.h>
#include <Update.h>
#include <esp_mac.h>
#include "esp_ota_ops.h"
#include "mbedtls/sha256.h"

// External LED Pin definitions
const int PIN_RED    = D2;
const int PIN_YELLOW = D3;
const int PIN_GREEN  = D4;
const int PIN_UNPAIR_BUTTON = D5; // Momentary button to GND; INPUT_PULLUP, so pressed reads LOW

// On-board RGB LED is defined by Arduino Nano ESP32 core (LED_RED, LED_GREEN, LED_BLUE: Active-LOW)

// BLE UUIDs
const char* BLE_SERVICE_UUID = "19B10000-E8F2-537E-4F6C-D104768A1214";
const char* BLE_CHAR_UUID    = "19B10001-E8F2-537E-4F6C-D104768A1214";
const char* BLE_AUTH_UUID    = "19B10002-E8F2-537E-4F6C-D104768A1214";
const char* BLE_VERSION_UUID = "19B10003-E8F2-537E-4F6C-D104768A1214";
// Firmware update (OTA) over BLE; protocol in docs/FIRMWARE_UPDATES.md.
// Commands and replies use separate characteristics: ArduinoBLE notifies the
// value a central writes, so a combined one would echo commands back as replies.
const char* BLE_OTA_CONTROL_UUID = "19B10004-E8F2-537E-4F6C-D104768A1214";
const char* BLE_OTA_STATUS_UUID  = "19B10005-E8F2-537E-4F6C-D104768A1214";
const char* BLE_OTA_DATA_UUID    = "19B10006-E8F2-537E-4F6C-D104768A1214";

// Release builds set this from the git tag (e.g. "1.2.0"); IDE builds report "dev".
#ifndef SIGNALLIGHT_VERSION
#define SIGNALLIGHT_VERSION "dev"
#endif

BLEService lightService(BLE_SERVICE_UUID);
BLEByteCharacteristic lightCharacteristic(BLE_CHAR_UUID, BLERead | BLEWrite | BLEWriteWithoutResponse | BLENotify);
BLEStringCharacteristic authCharacteristic(BLE_AUTH_UUID, BLERead | BLEWrite | BLENotify, 64);
// Read-only and not gated by AUTH: the version isn't sensitive, and the app reads it
// before deciding whether a firmware update is available.
BLEStringCharacteristic versionCharacteristic(BLE_VERSION_UUID, BLERead, 32);
BLEStringCharacteristic otaControlCharacteristic(BLE_OTA_CONTROL_UUID, BLEWrite, 100);
BLEStringCharacteristic otaStatusCharacteristic(BLE_OTA_STATUS_UUID, BLERead | BLENotify, 64);
BLECharacteristic otaDataCharacteristic(BLE_OTA_DATA_UUID, BLEWrite | BLEWriteWithoutResponse, 512);

// NVS Persistent Storage
Preferences prefs;
bool isPaired = false;
String deviceName = "";
String sharedSecret = "";

// Must be a global, never modified after BLE.setLocalName(): ArduinoBLE stores the raw
// pointer instead of copying the string, and re-reads it every time advertising is
// rebuilt (each BLE.advertise(), e.g. after a disconnect). A local String in setup()
// is freed when setup() returns, so later advertisements carried a garbage name and
// the dashboard's scan couldn't find the device until it was rebooted.
String advertisedName = "";

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

// Unpair button (hold to factory reset)
const unsigned long UNPAIR_HOLD_MS      = 10000; // Hold time required to factory reset
const unsigned long BUTTON_DEBOUNCE_MS  = 50;
bool unpairHoldActive = false;      // True while the button is held; freezes the display on red
bool buttonDown = false;            // Debounced button state
bool lastRawButtonDown = false;
unsigned long lastRawButtonChange = 0;
unsigned long buttonPressStart = 0;

// Firmware update (OTA) state. BLE event handlers only change this state and call
// Update; all BLE notifications and LED changes happen in loop(), because doing
// them inside a handler can re-enter BLE.poll().
const uint32_t OTA_ACK_EVERY          = 16;     // App keeps up to 32 chunks in flight (see throughput results)
const unsigned long OTA_IDLE_TIMEOUT_MS    = 15000;  // Abort if data stops arriving
const unsigned long OTA_CONFIRM_TIMEOUT_MS = 300000; // Roll back new firmware not confirmed within 5 min
bool otaActive = false;
uint32_t otaSize = 0;
uint32_t otaReceived = 0;
uint32_t otaExpectedSeq = 0;
unsigned long otaLastDataAt = 0;
uint8_t otaExpectedSha[32];
mbedtls_sha256_context otaSha;
bool otaAckPending = false;
uint32_t otaAckSeq = 0;
String otaReplyPending = "";
unsigned long otaRebootAt = 0;   // Non-zero once a verified image is installed; reboot at this time
bool otaDisplayShown = false;
bool firmwarePendingVerify = false; // First boot of an OTA image, not yet confirmed good

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
  // While the unpair button is held, the LEDs are frozen on red. Incoming commands
  // still update activeColor (via setActiveColor), so the right color is restored if
  // the hold is cancelled — they just don't reach the LEDs until then. This also keeps
  // the watchdog from overwriting the red mid-hold. A firmware update freezes the
  // display the same way, from BEGIN until the reboot.
  if (unpairHoldActive || otaActive || otaRebootAt != 0) {
    return;
  }
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

void showUnpairHoldRed() {
  digitalWrite(PIN_YELLOW, LOW);
  digitalWrite(PIN_GREEN, LOW);
  digitalWrite(PIN_RED, HIGH);
  setOnboardRGB(true, false, false);
}

// Non-blocking: called every loop(). Hold the button for UNPAIR_HOLD_MS to factory
// reset; the light shows solid red the whole time it's held. Releasing early cancels
// and restores whatever color is currently active.
void handleUnpairButton() {
  unsigned long t = millis();
  bool rawDown = digitalRead(PIN_UNPAIR_BUTTON) == LOW;

  if (rawDown != lastRawButtonDown) {
    lastRawButtonDown = rawDown;
    lastRawButtonChange = t;
  }

  if (t - lastRawButtonChange >= BUTTON_DEBOUNCE_MS && rawDown != buttonDown) {
    buttonDown = rawDown;
    if (buttonDown) {
      buttonPressStart = t;
      unpairHoldActive = true;
      showUnpairHoldRed();
      Serial.println("[Reset] Unpair button held. Keep holding for 10s to factory reset...");
    } else if (unpairHoldActive) {
      unpairHoldActive = false;
      displayedColor = ' '; // Force applyColor to re-drive the LEDs
      applyColor(isPaired ? activeColor : 'Y');
      Serial.println("[Reset] Unpair button released early. Cancelled.");
    }
  }

  if (unpairHoldActive && t - buttonPressStart >= UNPAIR_HOLD_MS) {
    Serial.println("[Reset] Unpair button held for 10s. Factory resetting...");
    // Turn the solid red off briefly so the 3 confirmation flashes read as distinct.
    digitalWrite(PIN_RED, LOW);
    setOnboardRGB(false, false, false);
    delay(400);
    factoryReset(); // Flashes red 3 times, wipes flash, reboots unpaired
  }
}

// The core marks a newly installed image as good before setup() runs unless this
// returns true. Returning true makes the sketch confirm it itself (on the first
// authenticated connection, see processAuthMessage), so a firmware that boots but
// can't talk BLE gets rolled back. Defined weak in the core's C code, hence extern "C".
extern "C" bool verifyRollbackLater() {
  return true;
}

bool parseSha256Hex(const char* hex, uint8_t out[32]) {
  if (strlen(hex) != 64) {
    return false;
  }
  for (int i = 0; i < 32; i++) {
    char byteStr[3] = {hex[2 * i], hex[2 * i + 1], 0};
    char* end;
    out[i] = (uint8_t)strtoul(byteStr, &end, 16);
    if (*end != 0) {
      return false;
    }
  }
  return true;
}

// Stops an update in progress without installing anything. Safe to call from
// BLE handlers: no BLE calls or LED changes here (loop() restores the display).
void otaReset() {
  if (otaActive) {
    Update.abort();
    mbedtls_sha256_free(&otaSha);
  }
  otaActive = false;
  otaAckPending = false;
}

void otaFail(const String& reason) {
  Serial.println("[OTA] Failed: " + reason);
  otaReset();
  otaReplyPending = "ERR " + reason;
}

void otaBegin(const String& cmd) {
  unsigned long size = 0;
  char shaHex[65] = {0};
  if (sscanf(cmd.c_str(), "BEGIN %lu %64s", &size, shaHex) != 2 || size == 0 || !parseSha256Hex(shaHex, otaExpectedSha)) {
    otaReplyPending = "ERR BAD_BEGIN";
    return;
  }
  otaReset();
  if (!Update.begin(size)) {
    otaReplyPending = String("ERR BEGIN ") + Update.errorString();
    return;
  }
  mbedtls_sha256_init(&otaSha);
  mbedtls_sha256_starts_ret(&otaSha, 0);
  otaActive = true;
  otaSize = size;
  otaReceived = 0;
  otaExpectedSeq = 0;
  otaLastDataAt = millis();
  Serial.printf("[OTA] Update started: %lu bytes\n", size);
  otaReplyPending = "READY";
}

void otaEnd() {
  if (!otaActive) {
    otaReplyPending = "ERR NOT_ACTIVE";
    return;
  }
  if (otaReceived != otaSize) {
    otaFail("SIZE " + String(otaReceived) + "/" + String(otaSize));
    return;
  }
  uint8_t digest[32];
  mbedtls_sha256_finish_ret(&otaSha, digest);
  if (memcmp(digest, otaExpectedSha, sizeof(digest)) != 0) {
    otaFail("SHA_MISMATCH");
    return;
  }
  mbedtls_sha256_free(&otaSha);
  // Validates the image and makes it the boot partition for the next restart.
  if (!Update.end()) {
    otaActive = false; // Update already cleaned up; don't abort it again
    otaReplyPending = String("ERR END ") + Update.errorString();
    Serial.println("[OTA] Failed: " + otaReplyPending);
    return;
  }
  otaActive = false;
  otaRebootAt = millis() + 1000; // Give the OK notification time to go out
  Serial.println("[OTA] Image verified and installed. Rebooting...");
  otaReplyPending = "OK";
}

void onOtaControlWritten(BLEDevice, BLECharacteristic) {
  String cmd = otaControlCharacteristic.value();
  cmd.trim();
  // Only a paired light with an authenticated app accepts firmware; otherwise any
  // nearby BLE device could flash it.
  if (!isPaired || !isAuthenticated) {
    otaReplyPending = "ERR NOT_AUTHENTICATED";
    return;
  }
  if (otaRebootAt != 0) {
    otaReplyPending = "ERR REBOOTING";
  } else if (cmd.startsWith("BEGIN ")) {
    otaBegin(cmd);
  } else if (cmd == "END") {
    otaEnd();
  } else if (cmd == "ABORT") {
    otaReset();
    otaReplyPending = "ABORTED";
  } else {
    otaReplyPending = "ERR UNKNOWN_COMMAND";
  }
}

// Chunk format: [seq uint32 little-endian][image bytes].
void onOtaDataWritten(BLEDevice, BLECharacteristic c) {
  if (!otaActive) {
    return;
  }
  int len = c.valueLength();
  if (len < 5) {
    otaFail("SHORT_CHUNK");
    return;
  }
  const uint8_t* v = c.value();
  uint32_t seq = (uint32_t)v[0] | ((uint32_t)v[1] << 8) | ((uint32_t)v[2] << 16) | ((uint32_t)v[3] << 24);
  if (seq != otaExpectedSeq) {
    otaFail("SEQ expected " + String(otaExpectedSeq) + " got " + String(seq));
    return;
  }
  size_t n = len - 4;
  if (otaReceived + n > otaSize) {
    otaFail("TOO_MUCH_DATA");
    return;
  }
  if (Update.write((uint8_t*)(v + 4), n) != n) {
    otaFail(String("WRITE ") + Update.errorString());
    return;
  }
  mbedtls_sha256_update_ret(&otaSha, v + 4, n);
  otaReceived += n;
  otaExpectedSeq++;
  otaLastDataAt = millis();
  if (otaExpectedSeq % OTA_ACK_EVERY == 0 || otaReceived == otaSize) {
    otaAckSeq = seq;
    otaAckPending = true;
  }
}

// Called every loop(): sends queued replies, enforces the idle timeout, shows the
// update on the LEDs, reboots after a successful update, and rolls back new
// firmware that was never confirmed.
void handleOta() {
  if (otaAckPending) {
    otaAckPending = false;
    otaStatusCharacteristic.writeValue("ACK " + String(otaAckSeq));
  }
  if (otaReplyPending.length() > 0) {
    Serial.println("[OTA] -> " + otaReplyPending);
    otaStatusCharacteristic.writeValue(otaReplyPending);
    otaReplyPending = "";
  }
  if (otaActive && millis() - otaLastDataAt > OTA_IDLE_TIMEOUT_MS) {
    otaFail("TIMEOUT");
  }

  bool showOta = otaActive || otaRebootAt != 0;
  if (showOta && !otaDisplayShown) {
    otaDisplayShown = true;
    setOnboardRGB(false, false, true); // Onboard blue; external light keeps its color
  } else if (!showOta && otaDisplayShown) {
    otaDisplayShown = false;
    displayedColor = ' '; // Force applyColor to re-drive the LEDs
    applyColor(isPaired ? activeColor : 'Y');
  }

  if (otaRebootAt != 0 && (long)(millis() - otaRebootAt) >= 0) {
    BLE.end();
    delay(100);
    ESP.restart();
  }

  if (firmwarePendingVerify && millis() > OTA_CONFIRM_TIMEOUT_MS) {
    Serial.println("[OTA] New firmware was never confirmed by an authenticated connection. Rolling back...");
    esp_ota_mark_app_invalid_rollback_and_reboot();
  }
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
      // An authenticated connection proves new firmware can still talk BLE and
      // pair, which is what's needed to update it again: keep it.
      if (firmwarePendingVerify) {
        esp_ota_mark_app_valid_cancel_rollback();
        firmwarePendingVerify = false;
        Serial.println("[OTA] New firmware confirmed good; rollback cancelled.");
      }
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
  if (otaActive) {
    Serial.println("[OTA] Central disconnected mid-update. Aborting; current firmware unchanged.");
    otaReset();
  }
}

void setup() {
  pinMode(PIN_RED, OUTPUT);
  pinMode(PIN_YELLOW, OUTPUT);
  pinMode(PIN_GREEN, OUTPUT);

  pinMode(LED_RED, OUTPUT);
  pinMode(LED_GREEN, OUTPUT);
  pinMode(LED_BLUE, OUTPUT);
  pinMode(LED_BUILTIN, OUTPUT);

  pinMode(PIN_UNPAIR_BUTTON, INPUT_PULLUP);

  Serial.begin(115200);
  delay(300);
  Serial.println("\n=========================================");
  Serial.println("SignalLight Controller (Nano ESP32)");

  // First boot of a firmware installed over BLE: it must be confirmed (see
  // verifyRollbackLater) or it's rolled back.
  esp_ota_img_states_t otaState;
  if (esp_ota_get_state_partition(esp_ota_get_running_partition(), &otaState) == ESP_OK &&
      otaState == ESP_OTA_IMG_PENDING_VERIFY) {
    firmwarePendingVerify = true;
    Serial.println("[OTA] Running newly installed firmware; waiting for an authenticated connection to confirm it (rolls back after 5 min otherwise).");
  }

  // 1. Read Factory Hardware MAC Address directly from eFuse
  uint8_t baseMac[6];
  esp_read_mac(baseMac, ESP_MAC_BT);
  char macSuffix[5];
  snprintf(macSuffix, sizeof(macSuffix), "%02X%02X", baseMac[4], baseMac[5]);
  String defaultName = "SignalLight-" + String(macSuffix);

  // 2. Load Saved Settings from Flash
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

  advertisedName = defaultName;
  if (isPaired && deviceName.length() > 0) {
    advertisedName = deviceName;
  }

  Serial.print("[Config] Firmware Version: ");
  Serial.println(SIGNALLIGHT_VERSION);
  Serial.print("[Config] Status: ");
  Serial.println(isPaired ? "PAIRED" : "UNPAIRED");
  Serial.print("[Config] Advertising Name: ");
  Serial.println(advertisedName);

  // 3. Initialize BLE (Only ONCE)
  if (!BLE.begin()) {
    Serial.println("ERR: BLE.begin() failed!");
  } else {
    BLE.setLocalName(advertisedName.c_str());
    BLE.setDeviceName(advertisedName.c_str());
    BLE.setAdvertisedService(lightService);

    lightService.addCharacteristic(lightCharacteristic);
    lightService.addCharacteristic(authCharacteristic);
    lightService.addCharacteristic(versionCharacteristic);
    lightService.addCharacteristic(otaControlCharacteristic);
    lightService.addCharacteristic(otaStatusCharacteristic);
    lightService.addCharacteristic(otaDataCharacteristic);
    otaControlCharacteristic.setEventHandler(BLEWritten, onOtaControlWritten);
    otaDataCharacteristic.setEventHandler(BLEWritten, onOtaDataWritten);
    BLE.addService(lightService);

    lightCharacteristic.writeValue((byte)displayedColor);
    authCharacteristic.writeValue(isPaired ? "STATUS:PAIRED" : "STATUS:UNPAIRED");
    versionCharacteristic.writeValue(SIGNALLIGHT_VERSION);

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

  // 1. Unpair button (hold 10s to factory reset)
  handleUnpairButton();
  handleOta();

  // 2. Poll BLE stack
  BLE.poll();

  // 3. Restart advertising safely after disconnect (outside the callback)
  // disconnectTime is set by blePeripheralDisconnectHandler during BLE.poll() just
  // above, i.e. possibly later in this very iteration than the stale `now` above —
  // use a fresh read so "now - disconnectTime" can't underflow (see step 9's comment).
  if (needAdvertise && (millis() - disconnectTime >= 200)) {
    needAdvertise = false;
    Serial.println("[BLE] Resuming advertising...");
    BLE.advertise();
  }

  // 4. Security Timeout Check (disconnect central if it didn't authenticate in 4s)
  // connectTime is set by blePeripheralConnectHandler during BLE.poll() just above,
  // possibly later in this same iteration than the stale `now` — use a fresh read so
  // this can't underflow and spuriously kick a central the instant it connects.
  if (isCentralConnected && isPaired && !isAuthenticated) {
    if (millis() - connectTime > AUTH_TIMEOUT_MS) {
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

  // 8. Visual Pulse when Unpaired and Awaiting Connection (paused while the unpair
  // button is held, since this writes the LEDs directly rather than via applyColor)
  if (!isPaired && !isCentralConnected && !unpairHoldActive) {
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
  //
  // Use a freshly-read timestamp here rather than the `now` snapshot taken at the top
  // of loop(), instead of just relying on it like the other checks above. lastHeartbeatTime
  // can be updated by step 6 (processControlCommand, via a fresh millis() call) LATER in
  // this same iteration than when `now` was captured. Since these are unsigned long, if
  // that later update ends up numerically greater than the stale `now` (which happens
  // whenever BLE.poll() or anything else above takes even 1ms), "now - lastHeartbeatTime"
  // underflows and wraps around to a huge value — far past HEARTBEAT_TIMEOUT_MS — causing
  // an immediate, spurious YELLOW revert in the very same instant a command was just
  // successfully received. This was a real, observed bug: the watchdog log line and the
  // "[Control] Received..." log line for the command that supposedly timed out would show
  // the identical millisecond timestamp. A fresh read here guarantees it's never earlier
  // than any update lastHeartbeatTime/disconnectTime could have received this iteration.
  if (isPaired) {
    unsigned long watchdogNow = millis();
    if (isCentralConnected) {
      // While connected: timeout if no heartbeat received for 60s
      if (watchdogNow - lastHeartbeatTime > HEARTBEAT_TIMEOUT_MS) {
        if (displayedColor != 'Y') {
          Serial.println("[Watchdog] Heartbeat timeout while connected. Reverting to YELLOW.");
          applyColor('Y');
        }
      }
    } else {
      // While disconnected: hold previous color for DISCONNECT_GRACE_MS (45s), then turn Yellow
      if (watchdogNow - disconnectTime > DISCONNECT_GRACE_MS) {
        if (displayedColor != 'Y') {
          Serial.println("[Watchdog] Disconnect grace period expired. Reverting to YELLOW.");
          applyColor('Y');
        }
      }
    }
  }
}
