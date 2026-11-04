# Chronicle Sidekick branding

Canonical branding assets for Chronicle Sidekick.

- `SidekickLogo.svg`: self-contained master logo for scalable use.
- `SidekickLogo.png`: 512 px lossless raster export.
- `SidekickLogo.webp`: 512 px optimized raster export.
- `SidekickC.webp`: transparent branded Chronicle C layer used by the animated `/download` logo.
- `SidekickSwirl.gif`: motion reference for the occasional three-dot swirl.

The three dots replace the previous robot while preserving the original Chronicle C shape. `SidekickSwirl.gif` documents the occasional swirl used by Chronicle's `/download` page.

Regenerate all application and tray icons from the repository root:

```sh
go generate .
```

The generators treat `SidekickLogo.png` as the canonical raster source for Windows, macOS, and tray assets.
