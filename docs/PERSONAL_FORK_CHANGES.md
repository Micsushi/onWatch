# Personal Fork Changes

This fork keeps upstream onWatch as the base, but it is tuned for my local Windows workflow and server1 homelab deployment.

## Local Windows Workflow

- `onwatch` on this machine now rebuilds from this repo before starting the dashboard.
- Running `onwatch` with no arguments or `onwatch restart` stops the previously installed onWatch process, builds the current repo, replaces the local binary, and starts the fresh build on port `9211`.
- The local wrapper stamps dev builds with a unique version string, so dashboard CSS and JavaScript cache-bust on each rebuild.
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

## Notifications

- Discord is available as a notification channel.
- Discord webhook settings live in dashboard settings.
- Discord webhook URLs are encrypted before storage when an encryption key is available.
- Settings includes a dedicated Discord test button.
- Discord delivery is used for quota, reset, and auth error notifications when enabled.
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

## Still Planned

- Reset-refresh activator: send one minimal provider-specific message after a quota reset to start providers that only begin timers after first use.
- Codex-first refresh activator rollout, followed by other weekly-limit providers.
- Peak-hour Discord warnings, capped at once per day per provider.
- Weekly burst warnings per provider at 20% used in 24 hours, then every additional 10%.
- Underuse warnings when a weekly quota is likely to go unused.
- Cross-service equivalent capacity metric for comparing Codex Pro capacity against lower-tier provider plans.
- Better Antigravity display for its separate quota model.

## Security Notes

- Treat Discord webhook URLs as credentials.
- Rotate any webhook pasted into chat or logs after testing.
- Keep `ONWATCH_TRUST_PROXY_AUTH` off for local installs unless a trusted reverse proxy is actually enforcing auth.
