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

The macOS build requires the Xcode command-line tools because its tray integration uses cgo. Windows and Linux builds use `CGO_ENABLED=0`.

## Releases

Push a semantic version tag such as `v0.1.0`. GitHub Actions runs the tests and platform build checks first, then creates or updates the matching GitHub Release with:

- a universal macOS `.app` zip,
- a Windows amd64 executable,
- a Linux amd64 tarball,
- SHA-256 checksums for every download.

The release builds are currently unsigned. You can also run the workflow manually to produce the same files as a temporary Actions artifact without creating a GitHub Release.
