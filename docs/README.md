# Flow reference

Use this index to start from the operational or code question, not from package layout. Every flow document includes failure branches and exact package/function entry points. The Simplified Chinese index is at [`zh-CN/README.md`](zh-CN/README.md).

| Question | Document | Primary implementation entry points |
| --- | --- | --- |
| What happens from a join request through challenge, pass, cooldown, or automatic ban? | [Applicant journey](applicant.md) | `internal/verify.(*Service).OnJoinRequest`, `internal/verify.(*Service).OnAnswer`, `internal/verify.(*Service).OnKernelAnswer` |
| Which commands and challenge buttons can an admin use, and how does the DM settings panel commit changes? | [Administrator flows](admin.md) | `internal/bot.(*Service).handlerRoutes`, `internal/panel.(*Panel).OnSettings`, `internal/panel.(*Panel).OnSettingsCallback` |
| What do ban, purge, mute, warn, and the sender-channel filter do when rights or API calls fail? | [Moderation](moderation.md) | `internal/moderate.(*Service).moderate`, `internal/moderate.(*Service).OnWarn`, `internal/moderate.(*Service).FilterChannelSenders` |
| How do feed polling, deduplication, edits, eviction, and rate-limit retries work? | [Feed polling and delivery](feed.md) | `internal/feed.(*Service).Run`, `internal/feed.postFeedItems`, `internal/feed.refreshTracked` |
| How does a token-only first start become an owner-claimed, registered, permission-checked service? | [Deployment](deployment.md) | `main.main`, `main.(*registrationService).EnsureOwnerClaim`, `internal/moderate.(*Service).CheckGroupSetup` |
| Why are verification timeouts safe during a Telegram/network outage or restart? | [Outage and recovery](outage-recovery.md) | `internal/verify.(*Service).RunHeartbeat`, `internal/verify.(*Service).onExpiry`, `internal/verify.(*Service).onRecovery` |
| What is in each state file, what survives restart, and how are unreadable/corrupt/unwritable files handled? | [State and persistence](state-persistence.md) | `internal/store.Load`, `internal/store.Write`, `internal/store.(*Settings).CommitGroup` |
| What can go in `config.json`, what is a settings-panel change instead, and what is the default? | [Configuration reference](configuration.md) | `internal/config.LoadConfig`, `internal/store.LoadBaseline` |
| Which build, race, CI, release, version, localization, and state-fixture gates apply to a change? | [Development, CI, and release](development.md) | `main.main`, `internal/i18n.TestProductionCodeContainsNoChineseStringLiterals`, state compatibility generator tests |
