# Chronicle Sidekick

Chronicle Sidekick is a small background app for macOS, Windows, and Linux. It watches World of Warcraft combat logs and uploads each play session to Chronicle automatically.

## Development

```sh
go test ./...
go build ./...
```

The macOS build requires the Xcode command-line tools because its tray integration uses cgo. Windows and Linux builds use `CGO_ENABLED=0`.

## Releases

Push a semantic version tag such as `v0.1.0`. The release workflow builds and packages Chronicle Sidekick for macOS, Windows, and Linux.
