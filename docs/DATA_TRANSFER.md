# Portable Data Transfer

onWatch can move and merge history between macOS, Linux, and Windows without copying credentials or replacing the destination database.

## Terminal workflow

Export one archive on each source machine:

```bash
onwatch data export --out mac-history.onwatch.zip
onwatch data export --out linux-history.onwatch.zip
```

Copy the archives to the destination machine, then import them together:

```bash
onwatch data import mac-history.onwatch.zip linux-history.onwatch.zip
```

Use `--db PATH` before `--out` or the archive paths when working with a non-default database:

```bash
onwatch data export --db /path/to/onwatch.db --out history.onwatch.zip
onwatch data import --db /path/to/onwatch.db history.onwatch.zip
```

Export refuses to overwrite an existing file. Import prints inserted, updated, and skipped totals for each archive.

## Settings workflow

Open **Settings > Data**.

- **Export History** downloads a timestamped `.onwatch.zip` archive.
- **Import History** accepts one or more `.onwatch.zip` archives and merges them into the local database.

## Merge behavior

Imports are additive. Existing destination settings win.

Every installation has a stable random origin ID, and every exported history row keeps its original identity. This means:

- A 10-token record from a Mac and a 100-token record from Linux are both kept, even if they have the same timestamp.
- Importing the same archive again does not duplicate its records.
- Exporting an imported record from an intermediate machine and importing it elsewhere still does not duplicate the original.
- Active sessions and reset cycles may be updated by a newer archive, but older archives cannot reduce their terminal times or maximum counters.

Import validates the archive version, checksum, SQLite integrity, and portable schema before merging. The destination changes are committed in one transaction, so a malformed archive cannot leave a partial import.

## Privacy scope

The archive contains provider history, API Integration usage history, provider account labels, and an allowlist of non-secret interface preferences.

It does not contain:

- Provider API keys or dashboard credentials
- Dashboard users, password hashes, or login sessions
- OAuth access or refresh tokens
- SMTP passwords or Discord webhook URLs
- Push notification private keys or subscriptions
- Arbitrary unknown settings

Notification delivery channels are disabled in the exported copy. Provider settings are limited to display mode, pace mode, source, Claude Code detection, and region. API Integration history can still include model names and user-supplied metadata, so inspect and protect the archive as personal usage data.
