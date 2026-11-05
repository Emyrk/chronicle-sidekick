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

Automatic upload defaults to `on_game_exit` for a reported WoW directory when
Chronicle has not supplied a directory-specific setting. Chronicle can explicitly
set `auto_upload` to `disabled` to turn it off. Sidekick only enables the default
when the directory's selected log format advertises support for that trigger.

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

For local debugging, Sidekick writes the most recently attempted heartbeat payload
to `last-heartbeat.json` in its data directory before sending the request. The
snapshot is formatted JSON, replaced on each heartbeat, and contains no OAuth
token.

## Local log format selection

Sidekick owns each WoW directory's Chronicle log format. It makes an initial
best guess from local build metadata and the directory's realmlist, persists
that selection in local state, and lets the user change it from the directory's
**Client:** tray action. Chronicle-managed configuration controls upload and
archive behavior but does not select the log format.

The heartbeat reports Chronicle's canonical log-format wire value in
`log_format`. Sidekick currently selects among `1.12a-cc-addon`,
`hermesproxy_1_14_2_cc`, `2.4.3-cc-addon`, and `3.3.5a-cc-addon`.

## WoW directory inspection

Each reported WoW directory includes a fresh inspection of the allowlisted addons
and DLLs expected for its configured or detected log format. Sidekick sends the
inspection on every heartbeat so Chronicle can replace stale results.

```json
{
  "id": "wowdir_<sha256>",
  "path": "C:\\Games\\World of Warcraft",
  "tenant_name": "OctoPTR",
  "tenant_base_url": "https://octoptr.chronicleclassic.com",
  "realmlist": "192.168.1.214",
  "log_format": "1.12a-cc-addon",
  "inspection": {
    "status": "action_required",
    "checks": [
      {
        "id": "addon.chronicle_companion",
        "category": "addon",
        "name": "ChronicleCompanion",
        "required": true,
        "status": "missing"
      },
      {
        "id": "dll.nampower",
        "category": "dll",
        "name": "nampower.dll",
        "required": true,
        "status": "present"
      }
    ]
  }
}
```

Inspection status is `ready` when every required component is present,
`action_required` when at least one required component is missing, and `unknown`
when Sidekick does not know the directory's log format. Check status is
`present` or `missing`. Missing optional components do not make the directory
require action.

The current profiles are:

| Log format | Required | Optional |
| --- | --- | --- |
| `1.12a-cc-addon` | `ChronicleCompanion`, `nampower.dll` | `ClassicAPI.dll`, `SuperWoWhook.dll` |
| `hermesproxy_1_14_2_cc` | `ChronicleCompanionJimsProxy` | — |
| `2.4.3-cc-addon` | `ChronicleCompanionTBC` | — |
| `3.3.5a-cc-addon` | `ChronicleCompanionWoTLK` | — |

Addon checks look for directories under `Interface/AddOns`. DLL checks look for
regular files in the WoW installation root. Names are matched case-insensitively.
Sidekick does not enumerate or report arbitrary addons, DLLs, file contents, or
component versions.

## Reported issues

Each heartbeat replaces the installation's complete `issues` snapshot. Sidekick
reports an `upload_failed` issue as soon as Chronicle answers an upload with a
retryable HTTP server error. The issue is persisted across Sidekick restarts and
remains active until a combat log upload succeeds. The next heartbeat, sent at
most one minute later, removes the resolved issue from the settings page.

Issue details are sanitized before reporting. They include the HTTP status code
and standard status text, but never local file paths or arbitrary server response
bodies.

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

For the `1.12a-cc-addon` log format, Sidekick watches only the per-character
`CustomData/Chronicle_*.txt` files and uploads each file with Chronicle parser
format `1.12a-cc-addon` after the game exits. The wildcard suffix beginning at
the first underscore, such as `_Thrall.txt`, is persisted with the archive
group. A later session appends only to the recent group with the same suffix,
so characters in one installation retain independent Chronicle log groups.

For WoW Forever, Sidekick recognizes the `wowf` product in `.flavor.info`
(typically under `_forever_`), routes uploads to
`https://forever.chronicleclassic.com`, and selects `v22-cleu`. This takes
precedence over shared `.build.info` metadata for other installed clients.
It watches `Logs/WoWCombatLog*.txt` and supports uploading on game exit without
requiring a companion addon or Sidekick storage artifact.

For the `hermesproxy_1_14_2_cc` log format, Sidekick watches the standard
`Logs/WoWCombatLog*.txt` files and uploads with Chronicle parser format
`hermesproxy_1_14_2_cc`. Sidekick detects this format when the active
`wow_classic_era` row in `.build.info` reports version `1.14.2.42597`. It does
not guess from the flavor directory name alone.

For the `2.4.3-cc-addon` log format, Sidekick uploads with Chronicle parser format
`2.4.3-cc-addon`. TBC and Classic 1.14.2 uploads contain only the
gzip-compressed `combat_log` and do not capture or send a Sidekick storage
artifact.

For the `3.3.5a-cc-addon` log format, Sidekick uploads with Chronicle parser
format `3.3.5a-cc-addon` and sends two multipart parts when the SavedVariables
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
