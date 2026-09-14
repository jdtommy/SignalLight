/*
 * SignalLight - Arduino Nano ESP32 Firmware
 * 
 * Hardware:
 *   - Board: Arduino Nano ESP32 (ESP32-S3)
 *   - Pin D2: RED LED (In Meeting / Busy)
 *   - Pin D3: YELLOW LED (Away / Default)
 *   - Pin D4: GREEN LED (Available / Free)
 * 
 * Electrical Safety Note:
 *   The ESP32-S3 operates on 3.3V logic. If you are using a pull-up resistor
 *   on D3 to keep Yellow on by default, connect the 10k resistor to the 3.3V pin 
 *   (labeled 3V3) instead of 5V to keep within the ESP32's safe input voltage!
 *   Driving 12V LEDs should be done via logic-level N-MOSFETs or a driver module.
 * 
 * Control interfaces:
 *   1. Bluetooth Low Energy (BLE) - Local Name: "SignalLight"
 *      - Service UUID:        19B10000-E8F2-537E-4F6C-D104768A1214
 *      - Characteristic UUID: 19B10001-E8F2-537E-4F6C-D104768A1214
 *      Values: 'R' (Red), 'Y' (Yellow), 'G' (Green), '0' (Off), 'P' (Ping/Heartbeat)
 * 
 *   2. USB Serial (115200 baud)
 *      Commands: "RED\n", "YELLOW\n", "GREEN\n", "OFF\n", "PING\n"
 * 
 * Failsafe:
 *   If disconnected or no heartbeat received within 15 seconds,
 *   the system automatically defaults back to YELLOW ON.
 */

#include <ArduinoBLE.h>

// Pin definitions
const int PIN_RED    = D2;
const int PIN_YELLOW = D3;
const int PIN_GREEN  = D4;

// BLE UUIDs
const char* BLE_SERVICE_UUID = "19B10000-E8F2-537E-4F6C-D104768A1214";
const char* BLE_CHAR_UUID    = "19B10001-E8F2-537E-4F6C-D104768A1214";

BLEService lightService(BLE_SERVICE_UUID);
BLEByteCharacteristic lightCharacteristic(BLE_CHAR_UUID, BLERead | BLEWrite | BLEWriteWithoutResponse | BLENotify);

// Failsafe watchdog timer (15 seconds)
const unsigned long HEARTBEAT_TIMEOUT_MS = 15000;
unsigned long lastHeartbeatTime = 0;
char currentColor = 'Y';

void applyColor(char c) {
  currentColor = c;
  
  // Turn all OFF first (mutual exclusivity)
  digitalWrite(PIN_RED, LOW);
  digitalWrite(PIN_YELLOW, LOW);
  digitalWrite(PIN_GREEN, LOW);

  switch (c) {
    case 'R': // RED - In Meeting
      digitalWrite(PIN_RED, HIGH);
      Serial.println("[State] -> RED (In Meeting)");
      break;
    case 'G': // GREEN - Available
      digitalWrite(PIN_GREEN, HIGH);
      Serial.println("[State] -> GREEN (Available)");
      break;
    case 'Y': // YELLOW - Away / Default
    default:
      digitalWrite(PIN_YELLOW, HIGH);
      Serial.println("[State] -> YELLOW (Away / Default)");
      currentColor = 'Y';
      break;
    case '0': // OFF
      Serial.println("[State] -> ALL OFF");
      break;
  }

  // Update BLE characteristic value
  lightCharacteristic.writeValue((byte)currentColor);
}

void processCommand(char cmd) {
  cmd = toupper(cmd);
  lastHeartbeatTime = millis(); // Reset failsafe timer

  switch (cmd) {
    case 'R':
      applyColor('R');
      break;
    case 'Y':
      applyColor('Y');
      break;
    case 'G':
      applyColor('G');
      break;
    case '0':
      applyColor('0');
      break;
    case 'P': // Heartbeat Ping
      // Keepalive received, no color change needed
      break;
    case '?': // Query status
      Serial.print("STATUS:");
      Serial.println(currentColor);
      break;
    default:
      break;
  }
}

void setup() {
  // Initialize GPIO pins
  pinMode(PIN_RED, OUTPUT);
  pinMode(PIN_YELLOW, OUTPUT);
  pinMode(PIN_GREEN, OUTPUT);

  // Default state: YELLOW ON immediately
  applyColor('Y');
  lastHeartbeatTime = millis();

  // Initialize USB Serial for debugging and fallback
  Serial.begin(115200);
  delay(500);
  Serial.println("=========================================");
  Serial.println("SignalLight Controller (Nano ESP32)");
  Serial.println("Starting BLE advertising...");

  // Initialize BLE
  if (!BLE.begin()) {
    Serial.println("ERR: Starting BLE failed!");
  } else {
    BLE.setLocalName("SignalLight");
    BLE.setDeviceName("SignalLight");
    BLE.setAdvertisedService(lightService);

    lightService.addCharacteristic(lightCharacteristic);
    BLE.addService(lightService);

    // Initial characteristic value: 'Y'
    lightCharacteristic.writeValue((byte)'Y');

    BLE.advertise();
    Serial.println("BLE advertising active as 'SignalLight'");
  }
  Serial.println("Ready!");
  Serial.println("=========================================");
}

void loop() {
  // 1. Process BLE events
  BLE.poll();

  if (lightCharacteristic.written()) {
    byte val = lightCharacteristic.value();
    processCommand((char)val);
  }

  // 2. Process USB Serial commands
  if (Serial.available() > 0) {
    String input = Serial.readStringUntil('\n');
    input.trim();
    if (input.length() > 0) {
      char firstChar = toupper(input.charAt(0));
      processCommand(firstChar);
    }
  }

  // 3. Failsafe Watchdog:
  // If no communication received for HEARTBEAT_TIMEOUT_MS, revert to YELLOW
  if (millis() - lastHeartbeatTime > HEARTBEAT_TIMEOUT_MS) {
    if (currentColor != 'Y') {
      Serial.println("[Watchdog] Connection lost or timeout expired. Reverting to YELLOW.");
      applyColor('Y');
    }
    lastHeartbeatTime = millis(); // Avoid spamming
  }
}
