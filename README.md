# Chronicle Sidekick

Chronicle Sidekick is a small background app for macOS, Windows, and Linux. It watches World of Warcraft combat logs and uploads the latest log to the correct Chronicle community when the user selects **Upload now**.

Each selected WoW folder's `realmlist.wtf` determines its destination. One Sidekick can watch multiple WoW installations at the same time, and one Chronicle sign-in works for every supported community. With one client, the main upload action names it directly. With multiple clients, the upload action has one entry per WoW. The tray's **World of Warcraft folders** submenu also lets you upload a folder's logs, open its Chronicle site, or remove it. Each community supplies its own default log format and flavor, so the sidekick does not stamp those values on uploads.

| WoW client | Combat logs | Supported realmlists |
| --- | --- | --- |
| WoW Forever | `Logs/WoWCombatLog.txt` and unique combat logs | WoW Forever's configured Blizzard client |
| Vanilla 1.12a | `CustomData/Chronicle_<character>.txt` | OctoWoW, CapyCraft, VanillaPlus, Kronos |
| TBC 2.4.3 | `Logs/WoWCombatLog.txt` | Moonwell, Crusader Storm |
| Wrath 3.3.5a | `Logs/WoWCombatLog.txt` | ChromieCraft, Warmane |

Legacy clients may keep `realmlist.wtf` beside `Wow.exe` or under `Data/<locale>/realmlist.wtf`. The sidekick supports both locations.

When connected, Sidekick reports its app version, platform, configured WoW folder paths, routed Chronicle communities, and active health issues to Chronicle once a minute. Chronicle uses this status to show whether each connected computer is running and correctly configured.

## Development

```sh
go test ./...
go build ./...
```

The macOS build requires the Xcode command-line tools and the pinned Sparkle framework because its tray integration uses cgo:

```sh
sparkle_dir="$(scripts/setup-sparkle.sh)"
SPARKLE_DIR="$sparkle_dir" \
  CGO_LDFLAGS="-F$sparkle_dir -Wl,-rpath,@executable_path/../Frameworks" \
  go build .
```

Windows and Linux builds use `CGO_ENABLED=0`.

## Releases

Push a semantic version tag such as `v0.1.0`. GitHub Actions runs the tests and platform build checks first, then creates or updates the matching GitHub Release with:

- an ad-hoc-signed universal macOS `.app` zip with Sparkle,
- Windows amd64 and arm64 executables,
- Linux amd64 and arm64 tarballs,
- SHA-256 checksums and a project-owned ECDSA signature,
- a Sparkle appcast whose macOS archive is signed with Sparkle EdDSA.

Windows and Linux verify the signed checksum file before replacing the executable, then restart Sidekick. macOS uses Sparkle to verify, install, and relaunch the complete app bundle. The first Sparkle-enabled release must still be installed manually because older releases do not contain Sparkle. The macOS bundle is not notarized and has no Apple Developer ID, so Gatekeeper may still warn on the first manual installation.

### Project-owned update keys

Production packaging fails closed unless these GitHub Actions secrets exist:

- `UPDATE_SIGNING_KEY`: the PEM ECDSA private key corresponding to `internal/update/update-signing-cert.pem`.
- `SPARKLE_PRIVATE_ED_KEY`: the base64-encoded 32-byte Ed25519 seed corresponding to `packaging/sparkle-public-key.txt`.

The public Sparkle key is intentionally committed because clients need it to authenticate updates. Store both private keys outside the repository and add them directly to GitHub Actions secrets. Losing either private key prevents existing installations from authenticating normal key rotations, so keep encrypted offline backups. Manual workflow builds use the same project-owned keys but do not publish a release.
