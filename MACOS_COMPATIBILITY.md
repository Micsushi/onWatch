# macOS Compatibility Notes

Audit date: 2026-07-03

## Status

Strong macOS support is intended. The installer, docs, Keychain integration, and menubar companion all have macOS-specific paths. Native macOS build/test was not run because this audit was performed from Ubuntu.

## What Was Checked

- Static scan of Darwin build tags, installer logic, Homebrew setup, Keychain paths, and menubar code.
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
- Menubar builds require a macOS host and CGO.
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
