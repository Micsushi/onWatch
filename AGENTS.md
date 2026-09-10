# onWatch contributor guidance

## Scope

Read the README, relevant guide under `docs/`, and the affected tracked source
entrypoint before editing. Keep changes within the repository's fork boundary,
preserve upstream behavior where it remains supported, and protect local data,
credentials, and running processes.

## UI work

For UI work, inspect the current repository UI, source, and docs, then use a
relevant installed skill when available. Preserve project conventions,
accessibility, responsive behavior, and reversal behavior; keep UI copy concise
and actionable; validate the actual user flow. Do not require a new UI file or
private dependency solely to begin focused work.

## Command entrypoints

The tracked `app.sh` is the preferred project entrypoint for native workflows:

```text
./app.sh --build
./app.sh --test
./app.sh --smoke
```

The tracked Makefile provides `make build`, `make fmt-check`, `make vet`,
`make lint`, and `make test` for focused work. Use the command that matches the
change and inspect its output before reporting a result. Starting or stopping a
runtime is a separate action from documenting or checking a change.

## Secrets and data

Keep API keys, OAuth credentials, webhook URLs, `.env` files, SQLite data, and
collector state out of commits. User-facing instructions belong in the README
or `docs/`; contributor guidance belongs here. Do not commit temporary plans or
private runtime paths.
