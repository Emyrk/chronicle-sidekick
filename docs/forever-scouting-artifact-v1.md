# WoW Forever scouting artifact, version 1

Chronicle Sidekick may attach scouting data from ChronicleCompanionForever to a
WoW Forever combat-log upload. This artifact is distinct from the Wrath
`sidekick_storage` artifact.

## Multipart contract

The upload request includes the following optional multipart part:

| Property | Value |
| --- | --- |
| Field name | `forever_scouting` |
| Filename | `forever-scouting-v1.json` |
| Content type | `application/json` |

Sidekick sends the part on initial `upload-v2` requests and append requests when
an immutable archive snapshot contains at least one non-empty scouting source.
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
across accounts.

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
  "kind": "chronicle_companion_forever_scouting",
  "sources": [
    {
      "account": "ACCOUNT_DIR_NAME",
      "source": "WTF/Account/ACCOUNT_DIR_NAME/SavedVariables/ChronicleCompanionForever.lua",
      "scouting_lua": "{ ... exact validated Lua table expression ... }"
    }
  ]
}
```

Fields have these meanings:

- `schema_version` is the integer `1`.
- `kind` is the exact string `chronicle_companion_forever_scouting`.
- `sources` contains one entry for each account with non-empty scouting data.
- `account` is the source account directory basename.
- `source` is the slash-separated path relative to the Forever root.
- `scouting_lua` is the exact byte sequence of the validated `scouting` table
  expression, from its opening `{` through its matching `}`. JSON escaping is
  transport encoding only. Decoding the JSON string recovers the original Lua
  expression bytes.

Sidekick sorts `sources` by `account`, then by `source`, using bytewise string
ordering. Each account remains a separate entry.

## Omission and non-empty rules

A source is omitted when `scouting` is absent, `nil`, `false`, or an empty table
`{}`. A scouting table is non-empty when it contains at least one explicit or
implicit field. Field values such as `false`, `nil`, or the empty string still
count as entries. A non-table scouting value other than `nil` or `false` is
unsupported and causes that source to be skipped with a warning.

Sidekick creates `forever-scouting-v1.json` beside the immutable combat-log
snapshot. It does not modify, clear, rename, or delete the live
ChronicleCompanionForever SavedVariables source. Pending archive recovery uses
the retained JSON artifact, so retries after a Sidekick restart attach the same
snapshot.

## Chronicle processing requirement

Chronicle must treat `scouting_lua` as untrusted restricted Lua data. Chronicle
must parse it without executing Lua, loading it into a Lua runtime, evaluating
expressions, or invoking metatables. Unknown fields and collections should be
preserved or ignored according to Chronicle's parser version. They must not make
the payload executable.
