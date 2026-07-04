# Linux Compatibility Notes

Audit date: 2026-07-03

## Status

Docker build works on Ubuntu. Native build/test should work after installing Go 1.25.7 or newer, but Go is not installed on this host.

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

## Likely Changes Needed

- Document Ubuntu prerequisites:
  - Go 1.25.7+
  - Docker Compose
  - optional `libsecret-tools` for `secret-tool`
  - optional `iproute2` for `ss`
  - optional `net-tools` for `netstat`
- Mark Linux menubar as unsupported.
- Add a fresh-checkout Docker quickstart:

```bash
cp .env.docker.example .env
mkdir -p onwatch-data
sudo chown -R 65532:65532 onwatch-data
docker compose up -d
```

## Suggested Ubuntu Smoke Path

```bash
docker build --target runtime-shell -t onwatch-audit:local .
docker run --rm onwatch-audit:local --version
```
