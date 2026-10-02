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

**Protocol sketch** (new characteristic(s) on the existing service):

1. `BEGIN <size> <sha256>`: only accepted from an authenticated central. The device calls `Update.begin(size)` and replies `READY`.
2. Numbered data chunks. The device acknowledges each chunk (or each window of chunks), and the app only sends ahead within that window. Flow control borrows from Espressif's BLE OTA design and [esp-ota-ble](https://github.com/fl4p/esp-ota-ble).
3. `END`: the device checks the streamed SHA-256 and `Update.end(true)`, replies `OK` or an error, then reboots into the new image.

**Must not trust Windows write results.** WinRT can report a BLE write as successful when it never reached the device (see the `writeControl` history in `windows/ble/client.go`). Progress must be driven by the device's acknowledgements and the final hash check, never by write calls returning without error.

**While updating:** the light shows a distinct pattern (e.g. blue on the onboard RGB LED), and ignores color commands and the heartbeat watchdog until it reboots or the update is aborted.

**Firmware publishing:** the release workflow compiles the sketch with `arduino-cli` for `arduino:esp32:nano_nora` and attaches `signallight-firmware-<version>.bin` and its SHA-256 to the draft release. The app compares the light's reported version with the latest release.

## TODO

1. **Prototype throughput.** Push about 1.5MB over ArduinoBLE from the Windows app and time it (rough estimate: 1–5 minutes). This decides whether ArduinoBLE is fast enough or the NimBLE migration comes first.
2. **Firmware version reporting.** The light reports its version (e.g. in the `STATUS?` reply), and the dashboard displays it.
3. **CI builds firmware.** `arduino-cli compile` on each `v*` tag; attach the `.bin` and SHA-256 to the draft release.
4. **Device update receiver.** Update characteristic(s), chunk/ack protocol, `Update` library calls, "updating" LED pattern, and the rollback self-check.
5. **Windows update client and dashboard UI.** Check GitHub for newer firmware, offer click-to-update, allow manual `.bin` upload, show progress, and retry from scratch after a disconnect.
6. **USB recovery fallback (optional).** Flash over USB with `dfu-util` (the board's normal upload tool) if a light ever gets stuck.

## Later

- **Firmware signing.** CI signs the `.bin` (e.g. Ed25519) and the device verifies it against a public key built into the firmware before switching slots, so a compromised PC can't flash malicious firmware. Manual `.bin` uploads would then need signing too, or a developer-mode switch.
- **Windows app self-update** via the same GitHub Releases check.
- **Automatic (idle-time) firmware updates**, once click-to-update has proven reliable.
