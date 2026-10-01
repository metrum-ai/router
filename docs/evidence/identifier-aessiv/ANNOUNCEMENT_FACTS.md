# Next patch — identifier AES-SIV facts for announcement

Raw facts for the release that ships the `internal/aessiv` change. Distill
into customer-safe release notes / announcement copy at tag time; do not paste
this file into `docs-site/docs/release-notes/` until the version is shipped.

## One-liner

Rewrite-mode routers no longer crash under concurrent streaming tool-call id
rewrite after replacing abandoned `secure-io/siv-go` with in-tree pure-Go
AES-SIV-CMAC.

## Why

- Symptom: process exit `fatal error: fault` / SIGSEGV in
  `github.com/secure-io/siv-go` `aesCMacXORKeyStream` during SSE native id
  rewrite (coding agents / parallel tool calls).
- Root cause: upstream amd64 CMAC assembly is unsafe under concurrent router
  load; mutex alone was not enough once the bad path was hit in production
  traffic.
- Fix: drop `secure-io/siv-go`; implement RFC 5297 AES-SIV-CMAC in
  `internal/aessiv` (pure Go + `github.com/aead/cmac`); keep Seal/Open
  serialized on the shared AEAD; add concurrent round-trip tests.

## Customer impact

- Operators on `identifiers.mode: rewrite`: upgrade binary/image to stop
  mid-stream router deaths.
- No config change. No usage DB migration. Existing `mr_` tokens remain valid
  (same RFC 5297 wire format).
- Caller-visible API shapes unchanged.

## Validation bullets for release notes

- Concurrent rewrite-mode Chat / Anthropic tool streams complete without
  router process exit.
- Prior `mr_` tool-call ids still decode.
- `/readyz` and `/version` report the new package version.

## Changelog anchor

See `CHANGELOG.md` → `[Unreleased]` → Fixes (AES-SIV / `internal/aessiv`).
