# Sidekick events artifact, version 1

Chronicle Sidekick may attach collection data from ChronicleCompanionForever to a
WoW Forever combat-log upload. This artifact is distinct from the Wrath
`sidekick_storage` artifact.

The generic `sidekick-events` artifact carries addon event collections. Version 1
captures the complete Forever `ChronicleCompanionForeverDB.collections` table, including unknown future
collections such as battleground events. The `collections_lua` field is generic: it includes `scouting` and future
siblings such as battleground collections without interpreting their subfields.
New collections inside that container do not require a new artifact kind.

## Multipart contract

The upload request includes the following optional multipart part, matching
Chronicle PR [#2056](https://github.com/Emyrk/chronicle/pull/2056). Chronicle stores
it as artifact kind `forever-events`; the JSON envelope kind remains
`sidekick-events`.

| Property | Value |
| --- | --- |
| Field name | `forever_events` |
| Filename | `sidekick-events-v1.json` |
| Content type | `application/json` |

Sidekick sends the part on initial `upload-v2` requests and append requests when
an immutable archive snapshot contains at least one non-empty collections source.
If every account source is missing, empty, unsupported, or malformed, Sidekick
omits the multipart part. A bad optional source does not block combat-log upload.

## Source discovery

For a WoW Forever installation rooted at `<forever_root>`, Sidekick checks every
account directory at:

```text
<forever_root>/WTF/Account/<account>/SavedVariables/ChronicleCompanionForever.lua
```

The file must contain exactly one assignment to the global
`ChronicleCompanionForeverDB`. Sidekick ignores missing files. It rejects
symlinks and non-regular source files. The `account` value is the account
directory basename. Sidekick does not combine or deduplicate player GUID maps
across accounts. Sidekick reads and validates every regular, non-symlink candidate
and selects the one with the newest effective freshness. Effective freshness is
`ChronicleCompanionForeverDB.collections.last_updated` when it is a finite,
non-negative integer Lua number, interpreted as a Unix instant in seconds;
otherwise it is that file's filesystem modification instant (including subsecond
precision). Numeric strings, negative values, fractional values, and other types
are not valid markers. Equal effective instants are resolved by actual file
modification time (newest first), then account and source path in ascending
bytewise string order. A malformed candidate has no usable marker and uses its
file modification time. If selected, it produces a warning and no artifact. If
the selected file has missing, empty, or invalid collections, Sidekick omits the
artifact without falling back to an older account.

The marker was added in [addon commit
1908c7d](https://github.com/Emyrk/ChronicleCompanionForever/commit/1908c7d9f8360ab31f71ab3c9a903d25bf884844).
It is a wall-clock timestamp updated on collection mutations, including
initialization, not on settings changes or no-ops. It is not a sequence number
and is not guaranteed to increase monotonically. It is used only for account
selection, never as acknowledgement, snapshot identity, or reset authorization.
The complete collections table, including this marker and unknown fields, is
captured unchanged and cleared as a whole. A marker-only table is non-empty.

Sidekick validates the complete file as restricted, data-only Lua. It accepts
nested tables, implicit numeric entries, bracketed quoted or numeric keys,
identifier keys, escaped quoted strings, booleans, `nil`, numbers, comments,
field separators, and trailing commas. It rejects calls, operators, references,
and other executable expressions. Sidekick never executes the file.

## JSON schema

The version 1 envelope is:

```json
{
  "schema_version": 1,
  "kind": "sidekick-events",
  "sources": [
    {
      "account": "ACCOUNT_DIR_NAME",
      "source": "WTF/Account/ACCOUNT_DIR_NAME/SavedVariables/ChronicleCompanionForever.lua",
      "collections_lua": "{ ... exact validated Lua table expression ... }"
    }
  ]
}
```

Fields have these meanings:

- `schema_version` is the integer `1`.
- `kind` is the exact string `sidekick-events`.
- `sources` contains one entry for the selected newest account with non-empty,
  valid collections data. The array shape is retained for schema compatibility.
- `account` is the source account directory basename.
- `source` is the slash-separated path relative to the Forever root.
- `collections_lua` is the exact byte sequence of the validated `collections` table
  expression, from its opening `{` through its matching `}`. JSON escaping is
  transport encoding only. Decoding the JSON string recovers the original Lua
  expression bytes.

Sidekick never merges collections from different accounts.

## Omission and non-empty rules

A source is omitted when `collections` is absent, `nil`, `false`, or an empty table
`{}`. A collections table is non-empty when it contains at least one explicit or
implicit field. Field values such as `false`, `nil`, or the empty string still
count as entries. A non-table collections value other than `nil` or `false` is
unsupported and causes that source to be skipped with a warning.

## Capture, cleanup, and retry lifecycle

Every Forever upload path, including tray manual upload and addon RPC archives,
captures available flushed SavedVariables collections. Missing accounts are
ordinary absence. Invalid sources warn without blocking the combat log. There is
no fallback to the old `DB.scouting` field.

Sidekick prepares all currently pending logs before resetting shared sources.
Each log owns an immutable artifact in a unique archive subdirectory. The state
file records the exact combat-log/artifact pair, so append groups cannot overwrite
older retry artifacts or recover an artifact belonging to a different snapshot.
Retries and restart recovery use the retained artifact, not live SavedVariables.
Combat-only source-ID deduplication does not skip requests with a collections
artifact; the multipart request must reach Chronicle.

Before cleanup Sidekick re-reads and validates the latest complete source. It
syncs the combat-log archive, writes and syncs a new immutable JSON artifact for
every affected log, then persists their exact archive associations. Only after
all those steps succeed does it replace the captured `collections` value with
`{}` in the selected account only; all other accounts remain untouched.
Unrelated bytes, comments, settings (including `sidekick_enabled`), and
unknown fields outside `collections` are preserved. Earlier artifacts remain
retained in the same archive group.

Cleanup uses a same-directory temporary file, file sync, and `os.Rename`
replacement. Rename is atomic where the platform/filesystem supports it; Go does
not guarantee atomic rename on non-Unix platforms. Sidekick checks the full source bytes again immediately before replacement. A changed
source is re-read, captured into another durable artifact, and retried, preserving
the latest settings. Up to eight passes bound a continuously changing writer;
failure leaves the latest unreset source in place and logs a warning. Recapture
is pinned to the initially selected account for that drain, even if freshness markers or modification
times change; it never moves on to an older account.
Missing/empty logs, malformed sources, copy failures, artifact-write failures, and
state-persistence failures never authorize a reset.

**Cleanup is unconditional with respect to game process state**, including manual
uploads while WoW is running. This is the requested cleanup policy, not a claim
that writing live SavedVariables is safe. The running game may restore its
in-memory collections on its next flush. There is no cross-process lock or atomic
coordination with WoW: an external write between the final byte comparison and
rename can still race the replacement. Atomic replacement and byte comparisons
provide best-effort protection only. The manual-upload UI is handled separately.

## Chronicle processing requirement

Chronicle must treat `collections_lua` as untrusted restricted Lua data. Chronicle
must parse it without executing Lua, loading it into a Lua runtime, evaluating
expressions, or invoking metatables. Unknown fields and collections should be
preserved or ignored according to Chronicle's parser version. They must not make
the payload executable.
