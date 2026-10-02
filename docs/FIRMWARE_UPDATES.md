# Firmware Updates over Bluetooth (Design)

Status: **planned, not started.** Goal: update the light's firmware from the Windows app over Bluetooth, with no USB cable and no Wi-Fi setup.

## Decisions

- **Transport:** Bluetooth (BLE), reusing the existing pairing and `AUTH` handshake.
- **Firmware sources:** the latest GitHub Release (normal path), plus a manual `.bin` upload in the dashboard for testing local builds.
- **Updates are click-to-install.** The dashboard shows "firmware vX available" and the user starts the update. No automatic updates for now.
- **Firmware signing is deferred.** The device will verify a SHA-256 of the image, but not a publisher signature yet. See [Later](#later).
- **Windows app self-update is out of scope** for this work.

## What the hardware already gives us

Checked against the installed core (`arduino:esp32` `2.0.18-arduino.5`, board `nano_nora`):

- **Two app slots.** The default partition table (`app3M_fat9M_fact512k_16MB`) has `app0` and `app1` (3MB each) plus `otadata`. The light keeps running from one slot while the new image is written to the other, then reboots into it. An interrupted update leaves the old firmware untouched.
- **Rollback is available.** The bootloader is built with `CONFIG_BOOTLOADER_APP_ROLLBACK_ENABLE=y`. **Catch:** the Arduino core marks a new image as good before `setup()` runs. To make rollback meaningful, the sketch must override `verifyRollbackLater()` to return `true`, then call `esp_ota_mark_app_valid_cancel_rollback()` only after its own self-check passes (BLE started, `loop()` reached). Otherwise the next reboot reverts to the previous image.

## Approach

**Device side: a small custom protocol on the existing ArduinoBLE stack, using the core's `Update` library for flash writes.** The ready-made BLE update libraries ([BLEOTA](https://github.com/gb88/BLEOTA), [NimBLEOta](https://github.com/h2zero/NimBLEOta)) use the NimBLE/Bluedroid stacks, and the sketch uses ArduinoBLE; two BLE stacks can't run at once. Their uploaders are Python/web/mobile, not Go, so the Windows side is custom either way. `Update` handles slot selection, writing, and switching the boot partition. Migrating the sketch to NimBLE stays an option if ArduinoBLE proves too slow (it's lighter and faster, but a bigger change).

**Protocol sketch** (new characteristics on the existing service: one for commands, one for status notifications, one for data):

1. `BEGIN <size> <sha256>`: only accepted from an authenticated central. The device calls `Update.begin(size)` and replies `READY`.
2. Numbered data chunks sent with `WriteWithoutResponse`. The device sends a cumulative ACK every 16 chunks, and the app keeps at most 32 chunks unacknowledged (see results below). Flow control borrows from Espressif's BLE OTA design and [esp-ota-ble](https://github.com/fl4p/esp-ota-ble).
3. `END`: the device checks the streamed SHA-256 and `Update.end(true)`, replies `OK` or an error, then reboots into the new image.

**Must not trust Windows write results.** WinRT can report a BLE write as successful when it never reached the device (see the `writeControl` history in `windows/ble/client.go`). Progress must be driven by the device's acknowledgements and the final hash check, never by write calls returning without error.

**While updating:** the light shows a distinct pattern (e.g. blue on the onboard RGB LED), and ignores color commands and the heartbeat watchdog until it reboots or the update is aborted.

**Firmware publishing:** the release workflow compiles the sketch with `arduino-cli` for `arduino:esp32:nano_nora` and attaches `signallight-firmware-<version>.bin` and its SHA-256 to the draft release. The app compares the light's reported version with the latest release.

## Throughput prototype results (October 2026)

Measured with `arduino/BleThroughputTest` and `windows/cmd/blethroughput`: 128KB per run, Nano ESP32 to Windows 11. Every run delivered all data in order with a matching checksum.

- **Firmware is small:** the current SignalLight image is about **469KB** (`arduino-cli compile --fqbn arduino:esp32:nano_nora`), not the ~1.5MB first assumed.
- **MTU:** Windows negotiated **242**, so each write carries 239 bytes (235 of data after a 4-byte sequence number).

| Mode | KB/s | Time for 469KB |
|---|---|---|
| Acked writes (`Write`) | 2.0 | ~3m50s |
| No-response, window 4 | 7.3 | ~65s |
| No-response, window 8 | 14.8 | ~32s |
| No-response, window 16 | 27.3 | ~17s |
| No-response, window 32 | 54.7 | ~9s |
| No-response, window 32, with flash writes | 47.4 | ~10s |

**Conclusions:**
- **ArduinoBLE is fast enough. No NimBLE migration needed.**
- Speed is bound by the ACK round trip (about 65ms, steady across windows), not by link bandwidth: doubling the window doubles the speed. Acked writes pay that round trip on every write, which is why they are slow.
- Writing to flash costs only 2–13%.
- `WriteWithoutResponse` lost no data, even at window 32. When the device falls behind, the BLE link layer holds the sender back instead of dropping data (the ESP32 transport's 258-byte receive buffer blocks the controller until `loop()` reads it).
- **Use window 32** (device ACKs every 16 chunks) for the real protocol. Larger windows would likely be faster, but ~10s is already enough.

**Lessons for the real protocol:**
- **Put commands and replies on separate characteristics.** ArduinoBLE notifies subscribers of the value a central writes, so on a combined write+notify characteristic, the app receives its own commands back as if they were replies.
- On Windows, `adapter.Enable()` is just `RoInitialize`; calling `RoInitialize` yourself first makes `Enable()` fail with "Incorrect function" (`S_FALSE`).
- Pass `BLE.setLocalName()` a string that outlives `setup()`; ArduinoBLE stores the pointer.
- `Update.write` rejects an image whose first byte isn't `0xE9` (ESP image magic).

## TODO

1. ~~**Prototype throughput.**~~ Done: see results above. ArduinoBLE with a 32-chunk window sends the current firmware in about 10 seconds.
2. ~~**Firmware version reporting.**~~ Done: read-only characteristic `19B10003`, set from the `SIGNALLIGHT_VERSION` build define (`dev` for IDE builds). Release builds can set it with `arduino-cli compile --build-property "compiler.cpp.extra_flags=-DSIGNALLIGHT_VERSION=\"x.y.z\""` (verified). The Windows app discovers characteristics unfiltered so lights on older firmware still connect, and the dashboard shows the version.
3. ~~**CI builds firmware.**~~ Done: the release workflow's `firmware` job compiles with `arduino-cli` 1.5.1, pinned to `arduino:esp32@2.0.18-arduino.5` and `ArduinoBLE@2.1.0` (the versions used for local IDE builds), stamps the version from the tag, checks the version string is in the binary, and attaches `signallight-firmware-<version>.bin` (the app image, which starts with `0xE9`) plus a `.sha256`.
4. **Device update receiver.** Update characteristic(s), chunk/ack protocol, `Update` library calls, "updating" LED pattern, and the rollback self-check.
5. **Windows update client and dashboard UI.** Check GitHub for newer firmware, offer click-to-update, allow manual `.bin` upload, show progress, and retry from scratch after a disconnect.
6. **USB recovery fallback (optional).** Flash over USB with `dfu-util` (the board's normal upload tool) if a light ever gets stuck.

## Later

- **Firmware signing.** CI signs the `.bin` (e.g. Ed25519) and the device verifies it against a public key built into the firmware before switching slots, so a compromised PC can't flash malicious firmware. Manual `.bin` uploads would then need signing too, or a developer-mode switch.
- **Windows app self-update** via the same GitHub Releases check.
- **Automatic (idle-time) firmware updates**, once click-to-update has proven reliable.
