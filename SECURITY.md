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
