# Code Signing Policy

**Official repository:** https://github.com/jdtommy/SignalLight — this is the only source of authentic SignalLight releases.

**Signed artifacts:** the compiled Windows executable (`signallight.exe`) attached to each [GitHub Release](https://github.com/jdtommy/SignalLight/releases).

**Signing method:** binaries are signed using [Azure Trusted Signing](https://azure.microsoft.com/en-us/products/artifact-signing) under a Public Trust, individual-developer certificate profile. Each signature is issued by a Microsoft-managed CA chain ("Microsoft ID Verified CS EOC CA 04" or successor) and RFC 3161 timestamped, so signatures remain valid independent of the short-lived signing certificate's own expiry.

**Release process:** the repository owner ([@jdtommy](https://github.com/jdtommy)) starts a release by pushing a `v*` tag. The [release workflow](.github/workflows/release.yml) builds and signs `signallight.exe` on GitHub-hosted runners and attaches it to a draft release, which the owner reviews and publishes. The workflow signs in to Azure via GitHub OIDC; Azure only accepts tokens for this repository's `release` environment (restricted to `v*` tags and `main`), and that identity holds only the signer role on this one signing account. No other individuals have release or signing authority.

**Privacy:** see [PRIVACY.md](./PRIVACY.md).
