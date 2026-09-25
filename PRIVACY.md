# Privacy Policy

SignalLight does not collect, transmit, or store any personal data on any server. Everything runs entirely on your own computer and your own Arduino hardware:

- Zoom meeting status is detected locally by scanning window titles on your PC — no Zoom account, API, or OAuth token is used.
- Windows lock/unlock state is detected locally via the OS.
- The web dashboard runs only on `localhost` and is not reachable from the network.
- Pairing info (BLE device address, name, and shared secret) is stored only in a local config file on your own machine (`%APPDATA%\SignalLight\config.json`), never transmitted elsewhere.

**Optional Zoom webhook integration:** if you choose to configure a Zoom webhook (off by default), your own Zoom account sends meeting/presence events directly to a server you host and control. No third party operated by the SignalLight project is involved in that data flow.

This project has no analytics, crash reporting, or telemetry of any kind.

## Contact

Questions about this policy can be raised via a [GitHub issue](https://github.com/jdtommy/SignalLight/issues) on the project repository.
