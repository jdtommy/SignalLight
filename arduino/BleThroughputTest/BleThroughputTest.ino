/*
 * BLE throughput prototype for SignalLight firmware updates.
 * See docs/FIRMWARE_UPDATES.md (TODO step 1). Throwaway test sketch, not the
 * product firmware: no pairing or auth, and it never activates a new image.
 *
 * Pair it with windows/cmd/blethroughput. Protocol:
 *   ctrl  <- "START <totalBytes> <ackEvery> <flash 0|1>"   ctrl -> "READY"
 *   data  <- [seq uint32 LE][payload...] repeated until totalBytes received
 *   ctrl  -> "ACK <seq>" after every ackEvery chunks (0 = no ACKs)
 *   ctrl  -> "DONE <bytes> <ms> <fnv1a hex> <gaps> <maxChunk> <flashErrors>"
 * With flash=1 the payload is written to the inactive OTA slot via Update,
 * then discarded with Update.abort(), to measure the real flash-write cost.
 */

#include <ArduinoBLE.h>
#include <Update.h>

BLEService testService("19B10100-E8F2-537E-4F6C-D104768A1214");
BLECharacteristic dataChar("19B10101-E8F2-537E-4F6C-D104768A1214", BLEWrite | BLEWriteWithoutResponse, 512);
BLEStringCharacteristic ctrlChar("19B10102-E8F2-537E-4F6C-D104768A1214", BLEWrite | BLENotify, 64);

bool running = false;
bool useFlash = false;
uint32_t totalBytes = 0;
uint32_t ackEvery = 0;
uint32_t received = 0;
uint32_t expectedSeq = 0;
uint32_t lastSeq = 0;
uint32_t gaps = 0;
uint32_t maxChunk = 0;
uint32_t flashErrors = 0;
uint32_t hash = 2166136261u; // FNV-1a 32-bit offset basis
unsigned long firstChunkAt = 0;
unsigned long finishedAt = 0;

// Set in BLE event handlers, sent from loop(): notifying from inside a handler
// would re-enter BLE.poll().
volatile bool readyPending = false;
volatile bool ackPending = false;
volatile bool donePending = false;
String errorPending = "";

void setBlue(bool on) {
  digitalWrite(LED_BLUE, on ? LOW : HIGH); // Onboard RGB is active-LOW
}

void onCtrlWritten(BLEDevice, BLECharacteristic) {
  String cmd = ctrlChar.value();
  cmd.trim();
  if (!cmd.startsWith("START ")) {
    return;
  }
  unsigned long total = 0, every = 0, flash = 0;
  if (sscanf(cmd.c_str(), "START %lu %lu %lu", &total, &every, &flash) != 3 || total == 0) {
    errorPending = "ERR bad START";
    return;
  }
  if (running && useFlash) {
    Update.abort();
  }
  totalBytes = total;
  ackEvery = every;
  useFlash = flash != 0;
  received = 0;
  expectedSeq = 0;
  lastSeq = 0;
  gaps = 0;
  maxChunk = 0;
  flashErrors = 0;
  hash = 2166136261u;
  firstChunkAt = 0;
  finishedAt = 0;

  if (useFlash && !Update.begin(totalBytes)) {
    errorPending = "ERR Update.begin " + String(Update.getError());
    return;
  }
  running = true;
  setBlue(true);
  Serial.printf("[Test] START total=%lu ackEvery=%lu flash=%d\n", total, every, useFlash);
  readyPending = true;
}

void onDataWritten(BLEDevice, BLECharacteristic c) {
  if (!running) {
    return;
  }
  int len = c.valueLength();
  if (len < 5) {
    return;
  }
  const uint8_t* v = c.value();
  uint32_t seq = (uint32_t)v[0] | ((uint32_t)v[1] << 8) | ((uint32_t)v[2] << 16) | ((uint32_t)v[3] << 24);
  if (seq != expectedSeq) {
    gaps++;
  }
  expectedSeq = seq + 1;
  lastSeq = seq;
  if (firstChunkAt == 0) {
    firstChunkAt = millis();
  }

  const uint8_t* p = v + 4;
  int n = len - 4;
  for (int i = 0; i < n; i++) {
    hash ^= p[i];
    hash *= 16777619u;
  }
  received += n;
  if ((uint32_t)len > maxChunk) {
    maxChunk = len;
  }
  if (useFlash && Update.write((uint8_t*)p, n) != (size_t)n) {
    flashErrors++;
  }

  if (ackEvery > 0 && (seq + 1) % ackEvery == 0) {
    ackPending = true;
  }
  if (received >= totalBytes) {
    finishedAt = millis();
    running = false;
    donePending = true;
  }
}

void onDisconnected(BLEDevice central) {
  Serial.println("[Test] Central disconnected");
  if (running && useFlash) {
    Update.abort();
  }
  running = false;
  setBlue(false);
}

void setup() {
  Serial.begin(115200);
  pinMode(LED_RED, OUTPUT);
  pinMode(LED_GREEN, OUTPUT);
  pinMode(LED_BLUE, OUTPUT);
  digitalWrite(LED_RED, HIGH);
  digitalWrite(LED_GREEN, HIGH);
  setBlue(false);

  if (!BLE.begin()) {
    Serial.println("ERR: BLE.begin() failed");
    while (true) {
      delay(1000);
    }
  }
  BLE.setLocalName("SL-ThroughputTest"); // String literal: ArduinoBLE keeps the pointer
  BLE.setDeviceName("SL-ThroughputTest");
  BLE.setAdvertisedService(testService);
  testService.addCharacteristic(dataChar);
  testService.addCharacteristic(ctrlChar);
  BLE.addService(testService);

  dataChar.setEventHandler(BLEWritten, onDataWritten);
  ctrlChar.setEventHandler(BLEWritten, onCtrlWritten);
  BLE.setEventHandler(BLEDisconnected, onDisconnected);
  BLE.setEventHandler(BLEConnected, [](BLEDevice central) {
    Serial.print("[Test] Central connected: ");
    Serial.println(central.address());
  });

  BLE.advertise();
  Serial.println("[Test] Advertising as SL-ThroughputTest");
}

void loop() {
  BLE.poll();

  if (errorPending.length() > 0) {
    Serial.println("[Test] " + errorPending);
    ctrlChar.writeValue(errorPending);
    errorPending = "";
  }
  if (readyPending) {
    readyPending = false;
    ctrlChar.writeValue("READY");
  }
  if (ackPending) {
    ackPending = false;
    ctrlChar.writeValue("ACK " + String(lastSeq));
  }
  if (donePending) {
    donePending = false;
    if (useFlash) {
      Update.abort(); // Measurement only: never activate the written image
    }
    setBlue(false);
    char msg[64];
    snprintf(msg, sizeof(msg), "DONE %lu %lu %08lx %lu %lu %lu",
             (unsigned long)received, (unsigned long)(finishedAt - firstChunkAt), (unsigned long)hash,
             (unsigned long)gaps, (unsigned long)maxChunk, (unsigned long)flashErrors);
    Serial.printf("[Test] %s\n", msg);
    ctrlChar.writeValue(msg);
  }
}
