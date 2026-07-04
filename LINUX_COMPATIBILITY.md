# Linux Compatibility Notes

Audit date: 2026-07-03

## Status

Verified on Linux (2026-07-04). Native `go build ./...` and `go vet ./...` pass in a
`golang:1.25` container, and almost all test packages pass (agent, api, web, store,
tracker, notify, metrics, update, ...). Docker build works on Ubuntu.

Two pre-existing test issues remain and are **not** Linux-portability defects:

- `internal/store` permission tests only fail when the suite is run as **root**
  (root bypasses file permissions); they pass as a normal user.
- `internal/config/TestConfig_DefaultValues` is sensitive to a stray `.env` on the
  path (test-isolation flake), and the root integration package needs live service
  infra so it times out in a bare container.

## What Was Tested

Host:

- Docker and Docker Compose are installed.
- Go is not installed.

Docker build:

```bash
docker build --target runtime-shell -t onwatch-audit:local onWatch
```

Result:

- Build succeeded.
- Built binary reported `onWatch v2.11.48`.

Compose config:

```bash
docker compose -f docker-compose.yml config
```

Result:

- Failed because `.env` is missing. This is expected for a fresh checkout.

## What Should Work On Linux

- Docker image build.
- Docker runtime after creating `.env` and a writable data directory.
- Web dashboard on port 9211.
- Core provider polling with configured tokens.

## Linux Caveats

- Native build requires Go 1.25.7+.
- Linux keyring auto-detection may need `secret-tool`.
- Antigravity local port discovery expects `ss` or `netstat`.
- Menubar companion is macOS-only; Linux uses a no-op stub.
- Docker bind-mounted data directory needs UID 65532 ownership or a named volume.

## Changes Made (2026-07-04)

Documented in `README.md`:

- Linux notes: menubar/tray is macOS-only (no-op stub on Linux; use the web dashboard).
- Optional Ubuntu packages: `libsecret-tools` (`secret-tool`), `iproute2` (`ss`) /
  `net-tools` (`netstat`) for keyring and Antigravity port discovery.
- Docker bind-mount note: chown the host data dir to UID 65532 (the non-root
  container user) or use the named volume.

Native prerequisites remain: Go 1.25.7+ and Docker/Docker Compose.

## Suggested Ubuntu Smoke Path

```bash
docker build --target runtime-shell -t onwatch-audit:local .
docker run --rm onwatch-audit:local --version
```
