# Design Notes

This page explains the design decisions that the source code refers to.

## Record identity and incremental reads

Every usage record is stored under a key made of its source and the identifier
the tool writes for it: the message ID for Claude Code (or, when it is
missing, the `requestId` or `uuid` of the log line), the `response_id` of a
`token_usage_record` for Codex CLI, and the session ID plus message ID for
Gemini CLI. Inserting a record whose key already exists changes nothing.

For each log file the application also stores how far it has read. The next
scan continues from that position instead of reading the file again.

Two consequences follow:

- Scanning is idempotent. A second scan of unchanged logs adds nothing, and the
  verification tools under `cmd/` rely on this.
- Records stay in the database after their log file is deleted. Totals computed
  from the remaining log files can therefore be lower than the stored totals;
  the verification tools report this case as inconclusive instead of as a
  mismatch.

## Secret storage

Admin API keys are stored in the operating system's credential store: Windows
Credential Manager, the macOS Keychain, or a Secret Service provider on Linux.
No key is written to disk in plain text. On Linux without a Secret Service
provider, saving a key fails with an explicit error instead of falling back to a
file.

## Release binaries

Release archives are built by GitHub Actions on native Windows, macOS and Linux
runners. The binaries are not code signed and the macOS app is not notarized,
so each operating system asks for confirmation on the first start. The
[README](../README.md) describes how to open the app on each platform.
