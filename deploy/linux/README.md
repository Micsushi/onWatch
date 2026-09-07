# Linux collector

Run the collector as the user who owns the local usage logs and credentials.
The central Docker service and this native collector have separate roles.

1. Install the verified Linux binary at `~/.local/bin/onwatch-collector`.
2. Create `~/.onwatch/collector-spool` and `~/.config/onwatch` with mode 0700.
3. Enroll the device using the central server's `device create` command. Save
   its token to `~/.config/onwatch/device-token` with mode 0600.
4. Create `~/.config/onwatch/collector.env` with mode 0600:

   ```ini
   ONWATCH_COLLECTOR_SERVER_URL=https://your-ingest-host:9443
   ONWATCH_COLLECTOR_DEVICE_ID=your-device-id
   ONWATCH_COLLECTOR_TOKEN_FILE=/home/your-user/.config/onwatch/device-token
   ONWATCH_COLLECTOR_SPOOL_DIR=/home/your-user/.onwatch/collector-spool
   ```

5. Copy `onwatch-collector.service` to `~/.config/systemd/user/`. Run
   `systemd-analyze --user verify ~/.config/systemd/user/onwatch-collector.service`,
   `systemctl --user daemon-reload`, then
   `systemctl --user enable --now onwatch-collector`.
6. On an unattended server, enable user lingering with
   `sudo loginctl enable-linger USER`. Record its prior value for rollback.
7. Check `systemctl --user status onwatch-collector`,
   `journalctl --user -u onwatch-collector`, and central device freshness.

The service can read home-directory logs and credentials but only writes its
spool and private temporary files. Assign quota ownership after the old poller
has stopped; usage collection can run during migration.

For upgrades, stop this service, retain the previous binary, install the verified
replacement, and start the service. Verify fresh heartbeats and queue drain.
For rollback, restore the previous binary and restart. Preserve the spool and
token. Removing the runner means disabling this service and removing its unit;
disable lingering only if this migration enabled it and no other service needs it.
