# Personal Fork Changes

This fork keeps upstream onWatch as the base, but it is tuned for my local Windows workflow and server1 homelab deployment.

## Local Windows Workflow

- `onwatch` on this machine now rebuilds from this repo before starting the dashboard.
- Running `onwatch` with no arguments or `onwatch restart` stops the previously installed onWatch process, builds the current repo, replaces the local binary, and starts the fresh build on port `9211`.
- The local wrapper stamps dev builds with a unique version string, so dashboard CSS and JavaScript cache-bust on each rebuild.
- A Windows scheduled task can run `scripts/windows-background-watchdog.ps1` at login to keep the local fork running in the background.
- The watchdog checks hourly and calls the repo-backed `onwatch restart` wrapper if the installed process is not running.
- Temporary local build folders such as `.tmp-go/`, `.tmp-install/`, and `.tmp-local-preview/` are ignored by git.

## Dashboard

- The All dashboard is the default first tab.
- The All dashboard uses a compact two-column provider layout on desktop.
- Provider cards show only high-value quota cards in the All view.
- Removed summary noise from the All dashboard, including extra tightest-limit text, average tiles, and graph-like summary tiles.
- Each provider card stacks quota cards vertically.
- Provider card collapse/expand animation now pulls the body into the header instead of abruptly hiding content.
- Header actions are grouped on the right: refresh, notifications, settings, theme, and logout.
- Dashboard and settings footers were removed for personal use.
- Menubar upstream footer/support links were removed.
- Settings autosave when changed; the manual global Save Settings button was removed.
- Settings > General includes fork preferences for the default dashboard and All dashboard density.
- The default dashboard preference is stored in the app database, so it follows local and hosted installs.
- The All dashboard density can be switched between compact and comfortable without editing code.

## Notifications

- Discord is available as a notification channel.
- Discord webhook settings live in dashboard settings.
- Discord webhook URLs are encrypted before storage when an encryption key is available.
- Settings includes a dedicated Discord test button.
- Discord threshold delivery follows weekly pace state: orange is silent, red notifies on entry and after a configurable additional-usage step that defaults to 10 percentage points.
- Purple very-under-pace quotas notify at configurable local times, defaulting to 10:00 and 22:00.
- Red over-usage and purple under-usage Discord alerts can be enabled independently in Settings.
- Discord pace state and scheduled under-usage delivery are tracked independently per provider, quota, and account.
- Discord delivery remains available for reset and auth error notifications when enabled.
- 5-hour reset notifications are off by default and can be enabled separately from other reset notifications.
- Browser push and SMTP notification behavior remains available.

## Hosted Deployment

- The app can run from Docker Compose.
- The homelab deployment lives in `C:\Users\sushi\Documents\Github\ansible_homelab`.
- Server1 deployment is wired as a Stage 3 monitoring/security service.
- The intended server1 hostname is `onwatch.mshi.ca`.
- Hosted mode can use Authelia in front of onWatch.
- `ONWATCH_TRUST_PROXY_AUTH=true` allows hosted mode to skip built-in onWatch login when the trusted reverse proxy handles authentication.
- Local Windows mode keeps the existing onWatch login behavior by default.

## Implemented Config

- `ONWATCH_TRUST_PROXY_AUTH`: when true, the web server skips built-in login/session middleware and trusts the proxy-auth boundary.
- Discord settings are stored in the database under the `discord` setting.
- Notification channel settings now support `email`, `push`, and `discord`.
- Personal fork preferences are stored in the database under the `fork_preferences` setting.
- Notification settings support `notify_reset_five_hour`; it defaults to false.
- Notification settings support `notify_overuse`, `overuse_repeat_percent`, `notify_underuse`, and `underuse_times`; defaults enable both pace alerts, repeat red alerts every 10 additional percentage points, and check purple quotas at `10:00` and `22:00`.

## Still Planned

- Reset-refresh activator: send one minimal provider-specific message after a quota reset to start providers that only begin timers after first use.
- Codex-first refresh activator rollout, followed by other weekly-limit providers.
- Cross-service equivalent capacity metric for comparing Codex Pro capacity against lower-tier provider plans.
- Better Antigravity display for its separate quota model.

## Security Notes

- Treat Discord webhook URLs as credentials.
- Rotate any webhook pasted into chat or logs after testing.
- Keep `ONWATCH_TRUST_PROXY_AUTH` off for local installs unless a trusted reverse proxy is actually enforcing auth.
