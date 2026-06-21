# Security Policy

## Supported versions

The latest released version (and the `main` branch) receive security fixes.

## Reporting a vulnerability

Please report security issues **privately** — do not open a public issue for an
unpatched vulnerability.

Use GitHub's [private vulnerability reporting](https://github.com/Zakkaus/gentoo-zh-verify-bot/security/advisories/new)
(the repo's **Security → Report a vulnerability**), or contact the maintainer
[@Zakkaus](https://github.com/Zakkaus) directly.

You can expect an acknowledgement within a few days. Once a fix is ready it will be
released and your report credited, unless you prefer to stay anonymous.

## Operator notes

- The bot token (`BOT_TOKEN`) and the optional `GITHUB_TOKEN` come from the
  environment only. Keep `bot.env` at mode `0600` and never commit it; `config.json`
  holds no secrets.
- Admin and moderation commands are gated on Telegram group-admin status and **fail
  closed** on API errors; callback buttons verify the acting user. Only run the bot
  in groups whose admin set you trust.
- The bot uses long polling and needs no inbound network port.
- The `$STATE_DIRECTORY` holds runtime state (pending verifications, strikes, feed cursors). It must
  be **private to the bot's service user** and not writable by untrusted users; the provided systemd
  unit's `StateDirectory=` + `DynamicUser=` already enforce this.

## Static analysis (gosec)

CI runs `gosec` and **fails on any new finding outside the accepted classes below**. These classes
are accepted by design under the intended private-`StateDirectory=` systemd deployment and are
excluded in CI (`-exclude=G304,G703,G706`):

| Rule | Where | Why accepted |
| --- | --- | --- |
| **G304 / G703** — file path from a variable | state-file reads/writes and config load (`verify.go`, `feed.go`, `main.go`, `config.go`) | Paths come from the operator CLI (`--config`) or systemd `$STATE_DIRECTORY`, never from a Telegram user; the unit confines filesystem access. |
| **G706** — log taint | `log.Printf` of usernames / chat titles / paths (`verify.go`, `feed.go`, `main.go`) | Values are operator config or Telegram-supplied display strings written to journald (not a fragile log parser); no command/format injection. |

Any other gosec rule — or a path/log finding that ever reaches genuinely untrusted input — fails CI.
