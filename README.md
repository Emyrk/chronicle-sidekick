<div align="center">
  <a href="https://chronicleclassic.com">
    <img src="packaging/chronicle-logo.png" alt="Chronicle logo" width="160">
  </a>
  <h1>Chronicle Sidekick</h1>
  <p>A small tray app that sends Classic World of Warcraft combat logs to the right Chronicle community.</p>
  <p>
    <a href="https://chronicleclassic.com">Chronicle</a> ·
    <a href="https://github.com/Emyrk/chronicle-sidekick/releases/latest">Download</a> ·
    <a href="https://github.com/Emyrk/chronicle">Chronicle source</a>
  </p>
</div>

Sidekick runs on macOS, Windows, and Linux. Add one or more WoW folders, then select **Upload now**. Each folder's `realmlist.wtf` routes its logs to the correct Chronicle site, using one Chronicle sign-in across all supported communities.

| WoW client | Combat logs | Communities |
| --- | --- | --- |
| WoW Forever | `Logs/WoWCombatLog.txt` and unique logs | Configured Blizzard client |
| Vanilla 1.12a | `CustomData/Chronicle_<character>.txt` | OctoWoW, CapyCraft, VanillaPlus, Kronos |
| TBC 2.4.3 | `Logs/WoWCombatLog.txt` | Moonwell, Crusader Storm |
| Wrath 3.3.5a | `Logs/WoWCombatLog.txt` | ChromieCraft, Warmane |

Legacy `realmlist.wtf` locations beside `Wow.exe` and under `Data/<locale>/` are supported.

## Development

```sh
go test ./...
go build ./...
```

macOS requires the Xcode command-line tools and the pinned Sparkle framework:

```sh
sparkle_dir="$(scripts/setup-sparkle.sh)"
SPARKLE_DIR="$sparkle_dir" \
  CGO_CFLAGS="-F$sparkle_dir" \
  CGO_LDFLAGS="-F$sparkle_dir -Wl,-rpath,@executable_path/../Frameworks" \
  go build .
```

Windows and Linux builds use `CGO_ENABLED=0`.

## Releases

Push a semantic version tag such as `v0.1.0`. GitHub Actions tests and publishes signed macOS, Windows, and Linux artifacts plus checksums and the Sparkle appcast.

Production releases require `UPDATE_SIGNING_KEY` and `SPARKLE_PRIVATE_ED_KEY` in GitHub Actions. Keep encrypted offline backups. Losing either private key prevents normal update-key rotation.

The macOS app is ad-hoc signed, not notarized, so Gatekeeper may warn on the first manual installation.
