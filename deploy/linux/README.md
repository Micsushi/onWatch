# Linux collector

Run the collector as the desktop user who owns the local provider credentials.
Do not use root. The collector needs read access to usage logs and credentials;
only its spool directory needs write access.

1. Install the verified Linux binary at `~/.local/bin/onwatch`.
2. Create `~/.onwatch/collector-spool` and `~/.config/onwatch` with mode 0700.
3. Save the registered device token to `~/.config/onwatch/device-token` with
   mode 0600. Never place the token itself in a service argument.
4. Create `~/.config/onwatch/collector.env` with mode 0600:

   ```ini
   ONWATCH_COLLECTOR_SERVER_URL=https://your-designated-ingest-host
   ONWATCH_COLLECTOR_DEVICE_ID=your-registered-device-id
   ONWATCH_COLLECTOR_TOKEN_FILE=/home/your-user/.config/onwatch/device-token
   ONWATCH_COLLECTOR_SPOOL_DIR=/home/your-user/.onwatch/collector-spool
   ```

5. Copy `onwatch-collector.service` to `~/.config/systemd/user/`, then run
   `systemctl --user daemon-reload` and
   `systemctl --user enable --now onwatch-collector`.
6. Check `systemctl --user status onwatch-collector`,
   `journalctl --user -u onwatch-collector`, and the server's device freshness.
   User services normally stop at logout. An administrator may explicitly
   enable lingering when collection must continue without a login session.

For an upgrade, stop this user service, retain the previous binary, install the
verified replacement, and start the service. Verify heartbeats and queue drain.
Restore the previous binary and restart if verification fails. Preserve the
spool and token throughout. To remove the service, disable and stop it, remove
its unit file, and reload systemd. Retain queued data until acknowledged.

Quota assignments use exponential retry scheduling with 20% jitter, capped at
one hour (or the configured interval when longer). Schedules survive restart.
Cancellation does not increase the failure count. Upload retries and device
authentication pauses are independent of provider quota retries.
