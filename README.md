# Chronicle Sidekick

Chronicle Sidekick is a small background app for macOS, Windows, and Linux. It watches World of Warcraft combat logs and uploads the latest log to Chronicle when the user selects **Upload now**.

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
