# Code Signing Policy

**Official repository:** https://github.com/jdtommy/SignalLight — this is the only source of authentic SignalLight releases.

**Signed artifacts:** the compiled Windows executable (`signallight.exe`) attached to each [GitHub Release](https://github.com/jdtommy/SignalLight/releases).

**Signing method:** binaries are signed using [Azure Trusted Signing](https://azure.microsoft.com/en-us/products/artifact-signing) under a Public Trust, individual-developer certificate profile. Each signature is issued by a Microsoft-managed CA chain ("Microsoft ID Verified CS EOC CA 04" or successor) and RFC 3161 timestamped, so signatures remain valid independent of the short-lived signing certificate's own expiry.

**Release process:** releases are built and signed by the repository owner ([@jdtommy](https://github.com/jdtommy)) from the `main` branch. No other individuals or automated third-party services currently have release or signing authority.

**Privacy:** see [PRIVACY.md](./PRIVACY.md).
