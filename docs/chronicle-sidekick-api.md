# Chronicle API changes for Sidekick archives

Sidekick's managed configuration remains protocol version `1`, but the canonical
configuration now includes an archive policy:

```json
{
  "version": 1,
  "archive": {
    "enabled": true,
    "retention_days": 7
  },
  "wow_directories": {}
}
```

The default is archive retention enabled for seven days. Chronicle should always
serialize `retention_days` in the range 1 through 365, including when retention
is disabled. When disabled, Sidekick still creates an immutable managed snapshot
before upload, then deletes its local files after a successful upload. Pending or
failed uploads are never deleted.

The canonical empty-directory configuration above has SHA-256:

```text
03ba74a583c3e096527eb448a456583b975a0f8c32d8517c630ff3954e3528e4
```

## Status response

`PUT /api/v1/sidekick/status` may return a same-origin settings link alongside
the managed configuration:

```json
{
  "settings_url": "/settings/sidekick/installations/<installation_id>",
  "config_hash": "<sha256>",
  "config": {
    "version": 1,
    "archive": {
      "enabled": true,
      "retention_days": 7
    },
    "wow_directories": {}
  }
}
```

Sidekick accepts either a relative URL or an absolute URL with the same scheme
and host as its Chronicle OAuth server. Cross-origin and non-HTTP(S) links are
ignored. Chronicle should return `settings_url` on every successful heartbeat,
including responses where managed configuration is unchanged, so the tray's
**View Settings…** action remains available.

## Initial upload and append

Initial uploads continue to use:

```text
POST /api/v1/raidlogs/logs/upload-v2
```

Appends use Chronicle's existing endpoint:

```text
POST /api/v1/raidlogs/logs/<log_id>/append
X-Chronicle-Upload-ID: <uuid>
X-Chronicle-Chunk-Sequence: <positive integer>
```

Sidekick persists the `log_id` returned by the initial upload. If the same WoW
installation starts again within 15 minutes of its previous exit, its next
finalized combat log is appended to that `log_id`.

For `wotlk_3.3.5a`, Sidekick sends two multipart parts when the SavedVariables
file exists:

- `combat_log`: gzip-compressed combat log, as today.
- `sidekick_storage`: raw `ChronicleSidekickBridgeWoTLK.lua` with content type
  `text/plain`.

The SavedVariables source is:

```text
<wow_dir>/WTF/Account/*/SavedVariables/ChronicleSidekickBridgeWoTLK.lua
```

Sidekick sends `sidekick_storage` on both initial uploads and append requests.
Chronicle's `upload-v2` handler accepts the base artifact, and Chronicle PR
#1481 (`Support positional artifacts on appended logs`) added the same optional
field to `/logs/<log_id>/append`. Each appended artifact is associated with its
specific log-file part, allowing the parser to consume telemetry in append order.

When Sidekick archives an upload, it moves the selected
`ChronicleSidekickBridgeWoTLK.lua` into the managed group directory rather than
copying it. The original SavedVariables file is therefore removed after it is
captured, and WoW/addon code can create a fresh file for subsequent telemetry.
If the move fails, Sidekick leaves the source in place and uploads the combat log
without the optional artifact.

Successful initial and append responses must continue returning:

```json
{
  "log_id": "<uuid>"
}
```

A `url` field remains optional; Sidekick falls back to `/logs/<log_id>`.
