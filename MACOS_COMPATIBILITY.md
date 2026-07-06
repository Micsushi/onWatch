# macOS Compatibility Notes

Audit date: 2026-07-04

## Status

Strong macOS support is intended. The shared Go code builds/tests on this Mac
with `CGO_ENABLED=0`; CGO-enabled tests are currently blocked by the local
Command Line Tools SDK. The installer, docs, Keychain integration, and menubar
companion all have macOS-specific paths.

The shared (non-menubar) codebase was verified with `CGO_ENABLED=0 go build`,
`CGO_ENABLED=0 go vet`, and `CGO_ENABLED=0 go test` on macOS (2026-07-04). The
menubar companion is gated behind `menubar && darwin` build tags and requires a
macOS host with CGO.

## What Was Checked

- Native macOS `CGO_ENABLED=0 go build`, `go vet`, and `go test` for the shared code.
- Static scan of Darwin build tags, installer logic, Homebrew setup, Keychain paths, and menubar code.
- CGO-enabled `go test ./...` was attempted and failed at link time against the
  local Command Line Tools SDK 11.3 with missing `SecTrustCopyCertificateChain`.
- Linux audit built the Docker image successfully and verified the Linux binary, but that does not prove macOS native behavior.

## What Should Work On macOS

- Shell installer detects `Darwin`.
- `app.sh --deps` uses Homebrew for Go and git.
- `install.sh` supports macOS and self-daemonizes instead of systemd.
- Anthropic/Cursor credential auto-detection can use macOS Keychain through `security`.
- macOS menubar companion exists behind `menubar && darwin` build tags.
- Release docs list macOS ARM64 and AMD64 binaries.

## macOS Blockers / Caveats

- Go is required for native source builds.
- Menubar builds require CGO, a current macOS SDK/Command Line Tools install, and
  still need a focused native smoke.
- Homebrew tap note says a separate tap repository is required.
- Signed/notarized macOS release status was not verified.
- Docker builds are Linux containers; they are useful for server/dashboard runtime but not for validating the native menubar companion.

## Likely Changes Needed

- Document the exact local macOS build split:

```bash
./app.sh --deps
./app.sh --build
./onwatch --debug
```

- For menubar verification, document that it must run on macOS:

```bash
go test -tags=menubar ./internal/menubar ./...
./app.sh --build
./onwatch --debug
```

- Clarify signing/notarization status for distributed `.app` or binary artifacts.
- Keep Docker docs separate from native macOS menubar docs.

## Suggested macOS Smoke Path

```bash
./app.sh --deps
make test
make build
./onwatch --version
./onwatch --debug
```

Menubar-specific:

```bash
./app.sh --build
./onwatch menubar --help
```
