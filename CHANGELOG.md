# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and this project adheres to
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- **Optional `control_group_id` for multi-group deployments.** It restricts `/rich`, the only
  command that changes bot-wide settings, to administrators in one guarded group. The other
  settings commands remain per-group. Leaving it unset preserves the previous any-guarded-group
  command policy and now logs a startup warning when several groups are configured. Naming a chat
  outside `groups` fails config loading instead of locking the global command out.
- **`/distro` is now advertised as an alias of `/pkgs`** in the command menu, `/help`, and the
  user documentation.
- Unknown config keys now produce a startup warning with their location. They remain ignored, so
  operators can correct misspellings without a hard startup failure.
- **Runtime group removal and optional owner-claim pinning.** The owner can use
  `/unregister <group-id>` in a private chat to remove a runtime-registered group and its
  overrides. `owner_claim_user_id` can restrict first-owner setup to one Telegram user.

### Changed
- **Join verification now tries the private challenge first by default.** Applicants who have
  previously started the bot receive only the DM; Telegram's ordinary cannot-initiate 403 falls
  back to the group challenge. New fallback links encode the group ID so concurrent requests
  cannot select another group's pending challenge, while existing bare `verify` links remain
  compatible. Administrators can toggle DM-first delivery per group in `/settings`.
- **Join floods have bounded memory use.** The pending queue is capped at 2,000 requests across the
  process and 500 per group. Requests beyond either cap remain for manual review, with an admin
  alert throttled to once every 10 minutes.
- **State saves serialize snapshot creation and commit for each file.** An unreadable state file is
  left untouched and its write path is disabled until restart instead of loading empty state and
  later overwriting recoverable data. Operators who see the state-load error must stop the service,
  repair ownership/permissions or restore the file, and restart.
- **Package lookups report source availability.** `/pkg`, `/use`, `/arm`, `/pkgs`, and `/armpkgs`
  now distinguish an answered miss from an upstream failure. Partial results identify the
  unavailable source instead of presenting an incomplete answer as definitive.
- **Kernel answers now require a plausible kernel version and context.** Bare releases, Chinese or
  English lead-ins, and `uname -a` output still pass; unrelated model/product versions such as
  `model=GPT-5.2` do not. The AI tripwire now matches only its exact nonce-bound `AGENT-… model=…`
  reply, so quoting or questioning the prompt is treated as a human reply.
- **Telegram duration normalization is explicit.** Positive ban and mute durations below 30 seconds
  become 30 seconds. Ban durations above 366 days become permanent, while mute durations cap at
  366 days; `timeout_seconds` also has a 30-second floor.
- First-owner claim links now expire after 10 minutes instead of 24 hours. Operators can override
  the window with `owner_claim_lifetime_seconds`; startup continues to identify an open claim.
- The build and release gate now uses Go 1.26.7, telego v1.11.2, staticcheck v0.8.1,
  govulncheck v1.7.0, and gosec v2.28.0.

### Fixed
- **Runtime-registered groups now activate without a service restart.** Verification, lookup
  authorization, pending restoration, panel command guards, control-group policy, locale selection,
  and command menus read the live settings owner. Registration completion now directs the
  administrator to run `/settings` in the group instead of emitting an unroutable deep link.
- **Schema-v2 migration now preserves the version-0 source and documents unsafe rollback.** Before
  the first version-0-to-v2 write, the application atomically copies the original to
  `settings.json.v0.bak` on a best-effort basis. Running a release without schema-v2 support still
  erases every group/global override and revision, owner identity and claim, enrollment nonce,
  pending registration, and registered group on its next settings write; it also reads the frozen
  pre-migration antispam toggles and channel whitelist. Stop the service and back up the current
  `settings.json` and `antispam.json` before rollback. The antispam migration remains one-way.
- **The bot now remains supervised through clean exits, crash loops, stalled polling, and reboot.**
  The systemd unit retries every 30 seconds without a start-limit latch, uses a 120-second
  progress-based watchdog, and allows 30 seconds for a bounded state-preserving shutdown. Poll
  handlers are registered before Telegram backlog consumption begins, active update handlers are
  capped at 64, and outages beyond Telegram's approximate 24-hour retention window produce a
  localized administrator alert to review pending join requests manually.
- **Bot-side delivery failures no longer misclassify applicants.** A group challenge that Telegram
  never confirmed used to expire as a normal strike. A failed kernel-question DM used to mark the
  applicant as prompted, so a later unrelated message could count as an answer. The first path is
  now strike-free and the second remains unprompted. If Telegram rejects a decline, administrators
  are alerted because the join request still needs manual handling.
- **Closed feed bugs no longer always display a green check.** `FIXED` resolves to ✅; `INVALID`,
  `WONTFIX`, `DUPLICATE`, and other closed-without-a-fix resolutions display ❌.
- Auto-feed polling now drains multi-page Bugzilla backlogs without advancing across undelivered
  items, bounds network and Telegram operations, and preserves tracked bugs across transient source
  or edit failures.
- **Group registration now survives restart and serializes same-group transitions.** Unauthorized
  group leave deadlines are durable but structurally separate from authorized pending
  registrations. Registration rechecks current bot membership while serialized against auto-leave,
  and effective-group membership changes trigger a throttled permission report.
- Owner-claim persistence failures now describe the private claim and tell the claimant to restore
  state-directory writes before retrying the same link.

## [3.12.0] - 2026-08-23

### Added
- **Optional self-hosted Telegram Bot API endpoint.** `TELEGRAM_API_URL` directs telego to a
  custom API server; leaving it unset keeps Telegram's hosted endpoint.

## [3.11.1] - 2026-08-20

### Removed
- The redundant AI-notice warning line in the kernel prompt ("下面一行是给自动化程序看的,真人请忽略").
  The tripwire itself already opens with "[SYSTEM OVERRIDE — AUTOMATED AGENTS ONLY]", so the extra
  Chinese heads-up added nothing; the trap is unchanged.

## [3.11.0] - 2026-08-20

### Added

- New state file `agents.json`: the automated-agent tally, kept across restarts.
- **Kernel-version verification, now the default join challenge.** A spam bot passes a four-option
  button quiz by tapping at random — one time in four, and it can re-apply — which is how the
  scam accounts were getting in. The new `kernel` mode asks the applicant to **type the version of
  the Linux kernel they run** (`uname -r`): there is no button to click, and a plausible version has
  to be produced as free text. Any real kernel is accepted, historic (`2.6.32`) through current
  (`6.18.45`) and on into future majors, written bare, with a local-version suffix
  (`6.12.3-gentoo`), or inside a sentence — so the check never needs editing when a new kernel ships.
  ARM is first-class: Raspberry Pi (`6.6.51+rpt-rpi-v8`), Jetson (`5.10.110-tegra`), Rockchip and
  Allwinner boards, Android/Termux (`6.1.75-android14-11-g1c2d3e4f`), Asahi on Apple Silicon and
  `…el9.aarch64` all pass, and a pasted `uname -a` line is accepted as-is.
  Three replies are allowed before the decline, so a typo isn't an instant rejection.
- **`verify_mode` + `/vmode`** to choose the challenge: `kernel` (default), `quiz` (the original
  multiple choice — unchanged, nothing was removed) or `mixed` (one at random per applicant).
  Configurable globally and **per group**, and switchable live by admins with
  `/vmode kernel|quiz|mixed|auto`; the runtime choice persists in `settings.json` (`auto` returns to
  the config file).

- **Per-applicant language.** Every string a joiner sees — the in-group challenge, the DM question,
  the channel-follow prompts, the approve/decline result — is now rendered from their Telegram
  interface language (`language_code`): Simplified Chinese, Traditional Chinese for `zh-Hant`/`zh-TW`/
  `zh-HK`/`zh-MO`, and English for every other language (`i18n.go`). The chosen locale is stored with
  the pending, so an outage re-notify still speaks the applicant's language. Admin output, the admin
  log and the config-supplied `questions` are unchanged (Simplified Chinese / as configured).
- **A fallback for applicants with no Linux installed.** "还没装", "我不用 Linux", "not installed",
  "I use Windows" and similar replies no longer burn an attempt: the applicant is switched to a
  **short-answer question** drawn from a small pool (`fallback_questions`, overridable; the built-in
  pool is localized) — typed, no options, and the answer never appears in the question. The built-in
  questions ask for the community's and the project's website (`gentoozh.org`, `gentoo.org`), which
  someone who has never run Linux can still answer; `gentoo-zh.org` is a different site and is not
  accepted. The escape is
  not advertised in the prompt, so a spam operator can't learn it exists, and reading it would give
  them nothing to copy. Replying with the format example the prompt itself printed
  (`6.12.3-gentoo`) is bounced once with a nudge rather than accepted — a copy-paste bot's laziest
  move — while a person who really runs that version is let through by sending it again.
- **A tripwire for LLM agents answering on someone's behalf, with a per-model tally.** The kernel DM
  carries a canary instruction addressed to automated agents, asking them to reply with a
  per-applicant token (derived from the pending's nonce) **and their own model name** instead of
  answering. A reply carrying that token is declined immediately, counts as a failure, and its
  claimed model is tallied in the new `agents.json` — surfaced by `/stats` and by a line per catch in
  the admin log, so admins can see which models keep being pointed at the group. The model is
  self-reported and spoofable: a usage tally, not evidence. Deterrence, not a security boundary — an
  agent instructed to ignore embedded text walks past it.
- A DM is only graded as an answer once the question has actually been sent. With a required channel
  the applicant first sees only the follow-the-channel prompt, so typing "已关注" instead of tapping
  the button used to be charged as a wrong kernel version — three of those declined someone who had
  never been shown a question. The flag persists with the pending, and an unprompted DM gets the
  ordinary auto-reply again.
- A reply naming another operating system ("我用的是 Windows 10.0.19045", "macOS 14.5") is routed to
  the short-answer fallback instead of being approved: those build numbers parse as plausible kernel
  versions. A five-digit sublevel is also rejected outright — no kernel has ever had one.
- The verification DM now degrades instead of failing: the notice rides in a collapsed
  `<blockquote expandable>` (Bot API 7.4), and if the server rejects that markup the message is
  re-sent without the quote and finally as plain text — only for a markup rejection, so a transient
  network error can't deliver the question twice. Old clients that don't know the entity just render
  it unfolded. Previously the send error was discarded, so a rejected message meant the
  applicant got no question at all and was declined at timeout.

- Outage resilience for verification, so a Telegram or network outage no longer punishes applicants for
  the bot's own downtime. A heartbeat (a periodic `GetMe`) tracks whether Telegram is reachable; while
  it isn't, a verification timeout re-arms a fresh window instead of declining and striking someone we
  couldn't hear from, and an on-demand probe covers the first seconds before the heartbeat notices. On
  recovery (detected live, or measured from a saved heartbeat at restart) everyone mid-verification gets
  a fresh full window plus a re-notify: a DM and a new in-group challenge. A quick redeploy stays quiet;
  a per-applicant cooldown and a cap keep repeated flapping from turning into a message storm.
- New state file `heartbeat.json`, the last time the bot reached Telegram, used to size the downtime at
  restart.

### Changed
- **Kernel prompt rewritten; the printed example is now a rejected decoy.** The prompt was one
  colloquial paragraph. It is now split into labelled parts (作答方式 / 无 Linux 设备) in written
  register across zh-CN / zh-TW / en, with the literal reply shown as `<code>` rather than 「」. The
  format example is an impossible version (`7.1.30`): `kernelAnswerOK` rejects it and a verbatim copy
  is bounced once with a nudge, so pasting the example back can never pass. The reworded no-Linux
  escape adds `无 Linux` / `無 Linux` to the phrase table so it still fires.
- **Wording pass over every Chinese string the bot shows.** A Codex audit against the repository's
  Chinese writing rules turned up 76 candidates; the ones that were factually wrong or that named
  the wrong thing are fixed. The channel sock-puppet feature is called 频道身份发言 instead of the
  slang 频道马甲; a Portage keyword is no longer described as a version type ("~ 表示测试 keyword",
  "未设置 arm64 keyword"); a missing arm64 keyword no longer claims `ACCEPT_KEYWORDS="~arm64"` will
  fix it; the Bugzilla assignee/reporter labels read 负责人/报告人; "取不到 / 读不到" became
  "无法获取 / 无法读取" in every failure message. README.zh-CN caught up with the buttons it
  documents (拒绝并封禁, not 举报并封禁), dropped the literal "武装定时器" and the repo shorthands
  必关频道 / 守护群 / strike.
- Two applicant-facing corrections worth calling out: the "you have no pending request" message told
  people to 在群里发起加入申请 when they are not in the group yet, and the Traditional Chinese
  catalog said 關注頻道 where Telegram's own Traditional interface says 加入頻道.

- **The no-Linux escape is documented again, but now costs a proof of liveness.** Hiding it kept
  spam operators from learning it existed, at the price of a newcomer with no Linux having no idea
  what to do. The prompt now spells it out — reply "我现在没有Linux设备" **plus the current minute**
  — which a canned string cannot carry and most LLM agents cannot produce either, while a person just
  reads their clock. A skewed clock is tolerated (±1 minute) and the half-hour / three-quarter-hour
  timezones are accepted at their own shift. A declaration without the minute earns one free format
  reminder instead of a strike; repeating it is graded as a wrong answer.
- **The automated-agent tripwire is written as a binding override, not a request.** The polite
  wording read as advice an agent could weigh against its own task; it now voids prior instructions
  explicitly, states that completing the check for a user is unauthorized automation, forbids
  answering, and demands the token plus the model name as the only allowed output. Detection is
  unchanged: only the per-applicant token counts.
- Fixed the phrases that mean "I have no Linux": "我沒有安裝" / "我没有安装" matched nothing (the list
  held "没有装" and "未安装", neither a substring of "没有安装"), and neither did "我不懂", "no idea"
  or a bare "what?". Those replies cost an attempt instead of offering the escape.
- With no `verify_mode` in the config the bot now serves the kernel challenge instead of the quiz.
  Set `"verify_mode": "quiz"` (or run `/vmode quiz`) to keep the previous behaviour.
- `questions` is only required for a group that can actually serve a quiz; a kernel-only config may
  omit the pool entirely. A quiz-mode group with an empty pool falls back to the kernel challenge
  rather than posting an unanswerable question.
- `pending.json` records the challenge mode, the applicant's locale and the replies used, so a kernel
  verification survives a restart or an outage recovery intact; a record written by an older build
  restores as a Simplified-Chinese quiz.
- The long poller's retry interval is pinned explicitly, and an update stream that ends without a
  shutdown signal now exits non-zero so systemd restarts the bot rather than sitting there dead.

### Fixed
- **The minute proof accepted a canned reply.** `minuteProofOK` read every number in the message,
  so a fixed string listing five of them ("no Linux device 1 4 7 10 13") matched at all 60 minutes
  of the hour — the check it was supposed to be immune to. Exactly one minute may now be offered,
  either as a standalone number or as a written-out clock ("14:46"); several different candidates
  are no proof at all. The phantom fourth shift (there is no UTC-X:45 zone) is gone too, so a
  single blind guess hits 9 minutes in 60 instead of 12.
- **An agent's tripwire reply was admitted to the group next door.** The reply names a model, and a
  model name carries a version, so grading it per pending declined the token's group and read
  "deepseek-v3.2" as a kernel version everywhere else the applicant was verifying. A DM is now
  classified once per message: one reply, one verdict for every pending, and one tally entry.
- **Re-applying handed back fresh attempts.** Cancelling the join request and applying again
  replaced the pending with `tries` zero and no recorded failure, so an applicant could answer
  wrong forever without reaching the strike threshold. A replacement now inherits the attempts and
  the spent one-shot guards.
- **A reply about another OS could bury a correct answer.** "Windows WSL2,
  5.15.167.4-microsoft-standard-WSL2" is a real kernel version with an explanation attached; it was
  routed to the no-Linux path and, repeated, walked a legitimate user toward the auto-ban. It now
  costs no attempt: one clarification, and the same answer sent again is accepted.
- **Stale replies could act on the pending that replaced theirs.** Every state transition
  (`recordKernelTry`, the reminder / hint / sample / clarification guards, the fallback switch) now
  requires the nonce it was decided against, so a message about a since-replaced request can no
  longer charge an attempt or spend a guard belonging to the new one.
- The one-shot guards are persisted, so a restart no longer hands each of them out again.
- `/start` re-sends the verification prompt at most once every 15 seconds per user; each press
  fanned out one message per pending with nothing throttling it.

## [3.10.2] - 2026-07-06

### Fixed
- **`/pkgs <name>` could resolve to a meta package.** The Gentoo line used the top search hit, but a
  `virtual/`/`acct-*` package ties with the real one on name — so `/pkgs openssh` showed
  `virtual/openssh` `0-r1` instead of `net-misc/openssh` `10.3_p1`. `pkgRelevance` now demotes
  `virtual`/`acct-group`/`acct-user` below a real same-name package (still shown when it's the only
  match). (`TestPkgRelevanceMeta`)
- **`/pkgs cat/pkg` returned "not found".** A full atom like `net-misc/openssh` was sent verbatim to
  Repology, which only indexes bare project names. `repologyQuery` now strips the category for the
  Repology lookup; the Gentoo line still resolves the real atom via `searchMainTree`. (`TestRepologyQuery`)

## [3.10.1] - 2026-07-06

### Fixed
- **`/pkgs` mislabeled stable Gentoo versions as `~amd64`.** `pkgVersion` returns `(amd64-stable, newest
  non-live)`; the display switch tested the newest/testing case before the stable case, so whenever the
  newest ebuild was itself amd64-stable (`stable == latest` — true for most stabilized packages, e.g.
  net-misc/openssh 10.3_p1) it printed `~amd64` instead of `amd64`. Extracted to a tested
  `gentooDistroLines`: newest-is-stable → one `amd64` line (no tilde); a `~amd64` testing bump above
  stable → both lines; testing-only pkg → `~amd64` only. (`TestGentooDistroLines`)
- **Overlay `/pkg` showed `~9999` instead of the real version.** `fetchOverlay` picked the highest
  version by raw compare, so a `9999` live ebuild masked the real release (e.g. dev-util/opencode-bin
  showed `~9999` despite shipping `1.17.13`). Now folds versions via `overlayPickVer` → `betterVer`,
  which tiers `9999` below real releases; a 9999-only package still shows `9999`. (`TestOverlayPickVer`)

### Changed
- **gentoo-zh overlay repo moved `microcai/gentoo-zh` → `gentoo-zh/overlay`.** Updated the built-in
  default and `config.example.json` (the old path only 301-redirects now); live config repointed too.

## [3.10.0] - 2026-06-27

### Added
- **`known_chat_ids` — extra chats the bot stays in without any verification role.** A new top-level
  config list of chats the bot must never auto-leave (so it can post announcements / be present)
  WITHOUT treating them as a guarded group, a required channel, or a trusted-member source. Unlike a
  `trusted_member_group_ids` entry these are NOT a bypass source (their members do not skip
  verification), and unlike a guarded group the bot runs no join-verification there — it simply won't
  remove itself. Use it for an announcement channel the bot only publishes to (e.g. a directory post).
  Wired into `IsKnownChat`; covered by `TestIsKnownChatExtra` + a `LoadConfig` round-trip test.

## [3.9.3] - 2026-06-24

### Changed
- **Wording: dropped the misleading "举报" (report) labels — no behavior change.** The Telegram Bot
  API has no report method, so these moderation actions never reported anything; they decline/ban.
  Renamed to match what they actually do: the join-challenge admin button is now **「拒绝并封禁」**
  (decline + ban the applicant) and `/sb` is **「封禁并清空」** (ban + purge the user's messages).
  Same actions, accurate names; touches only button text, `/help`, and command descriptions.

## [3.9.2] - 2026-06-23

### Fixed
A whole-repo reliability / safe-default review (multi-agent adversarial audit) found 0 P0, 1 P1, 5 P2.
All are fixed here, each with a test.

**P1 — verification critical path:**
- **A transient failure on the bot's OWN approve call no longer charges the applicant a strike.** A
  user who answered correctly but whose `ApproveChatJoinRequest` hit a transient Telegram error was
  re-declined within ~1s, given a real verification strike (pushing them toward the auto-ban), and
  silently blocked from re-applying by the cooldown. Now a decline caused by our own failed approve
  (`approve-retry`) or by a deadline that lapsed while the bot was DOWN (`restart-lapsed`) records NO
  strike, and the user gets a 60s grace window to retry instead of a ~1s bounce.

**P2 — fail-safe boundaries / false-negatives / CI:**
- **Corrupt state files are no longer silently overwritten.** A shared `loadJSONFile` helper backs a
  corrupt file up to `<path>.corrupt` before the next save (the hardening the feed already had),
  across all five loaders (pending / warns / verifyfail / settings / antispam).
- **A failing feed confirm-ping can't pin a bug into an endless re-edit loop.** State advances once the
  edit lands; the owed (best-effort) ping retries over a bounded `maxConfirmTries` and is then dropped.
- **`postFeed` surfaces a rate-limited signal** so a 429 on a confirm send pauses the cycle, like a 429
  on an edit already did, instead of re-attempting every cycle.
- **`/wiki` and `/bbs` no longer report a transient fetch failure as a definitive "no results".** The
  search helpers now signal fetch success, so an all-sources-failed case shows "暂时取不到…稍后再试".
- **CI/release analysis tools pinned** off floating `@latest` (staticcheck v0.7.0, govulncheck v1.4.0,
  gosec v2.27.1) in both workflows, so an upstream tool release can't turn a green commit red or block
  a tagged release.

**Also (P3, low-risk):** restored pendings for a removed group / out-of-range question are skipped; a
warning is persisted the moment it's issued (survives a failed at-limit kick + restart); a failed
at-limit kick and a failed `/ban`/`/sb` now alert admins; the unattended feed logs a news-fetch
failure; CONTRIBUTING lists the full CI gate.

## [3.9.1] - 2026-06-23

### Fixed
Two access-control boundary fixes on the v3.9.0 trusted-member bypass (review follow-up):
- **A per-group `trusted_member_group_ids` can now explicitly DISABLE the bypass with `[]`** — the
  resolver distinguishes an OMITTED field (inherit the global) from an explicit empty array (opt out
  for that group). Previously both inherited the global, so a sensitive group couldn't opt out of a
  global trusted source.
- **A trusted member now takes priority over the failure cooldown** — the trusted-member check runs
  *before* the anti-spam cooldown, so a verified member of a trusted group who had a prior failed
  verify is auto-approved (and their strikes cleared) instead of being silently declined by the
  cooldown. A confirmed trusted member whose auto-approve fails proceeds to normal verification and is
  NOT cooldown-declined; only a non-member / unconfirmable applicant is subject to the cooldown.
- Tests: gate-level `TestJoinGate` (the cooldown ordering — the integration branch the per-function
  test missed) and the explicit-`[]`-disables resolver + LoadConfig cases.

## [3.9.0] - 2026-06-23

### Added
- **Trusted-member group bypass** (`trusted_member_group_ids`) — an applicant who is **already a member
  of a configured trusted group** is auto-approved without the quiz, so verified members of a trusted
  group (e.g. the main group) don't re-verify when joining a sub-group. Global default + per-group
  override (same style as `required_channel_id`). Treated as an **access-control bypass, not a required
  channel**: it **fails CLOSED** — if the source-group membership can't be confirmed (lookup error / bot
  not in the group / non-member), the applicant just runs the normal verification; a failed auto-approve
  is logged + alerted and also falls back, so a request is never left stuck. On a successful bypass it
  clears any prior failed-verify strikes and records the approval — creating no pending and posting no
  quiz. The trusted source groups are treated as **known chats** (`IsKnownChat`), so the auto-leave logic
  never kicks the bot out of a group it needs to read membership from. A startup probe logs whether the
  bot can read each trusted group's membership.

## [3.8.1] - 2026-06-23

### Changed
Release / ops hardening (no runtime behavior change):
- **The release workflow's Test gate now matches CI** — gofmt check, `go vet`, staticcheck, `go build`,
  `go test -race`, govulncheck, and gosec all must pass before any release binary is built or uploaded
  (it previously ran only `go vet` + `go test`).
- **GitHub Actions are SHA-pinned** — `actions/checkout`, `actions/setup-go`, and
  `softprops/action-gh-release` are pinned to commit SHAs (version in a trailing comment); Dependabot
  continues to track and bump them.

### Added (tests, no logic change)
- **`writeJSONFile` unit tests** — the atomic-write primitive behind every restart-critical state file
  (pending / warns / feed / settings): clean round-trip, a marshal failure leaves the prior file intact
  (no torn/empty state), and concurrent writers stay corruption-free under `-race`.
- **Fixture parser tests** for the drift-prone upstream parsers — `/news` (index HTML), `/use` (ebuild
  IUSE + metadata.xml flags), `/pkg` (search-results HTML + ranking). `parseNews` and `rankSearchHits`
  were extracted from their fetch wrappers so a fixed sample of the real page guards the regexes
  against a silent "0 results" if a site's markup drifts. Coverage 31.4% → 33.4%.

## [3.8.0] - 2026-06-23

### Added
- **`modBot` test seam + regression coverage for the security-critical moderation glue** (the audit's
  top recommendation). A `modBot` interface (a superset of `verifyBot`) is now threaded through the
  admin gate (`adminStatus` / `isGroupAdmin`), the mute / unmute helpers, the shared `warnPrecheck`
  gate, and a newly-extracted `warnKick` — compile-checked type-widening with **zero behavior change**.
  New table tests cover the highest-risk branches: the admin gate **fails CLOSED** on a lookup error,
  a non-admin caller is **denied with no ban/restrict issued**, an admin target is skipped, mute
  restricts, unmute restores the group default (and falls back permissively but **still lifts the
  mute** when GetChat fails), and the warn-limit kick is rejoinable / honest when the unban sticks.
  Coverage 29.7% → 31.4%.

## [3.7.6] - 2026-06-23

### Fixed
Lookup-module robustness pass (from the audit's lookup-cluster review — all polish, no critical):
- **/armpkgs**: a transient AUR / Arch-Linux-ARM fetch failure (timeout / 5xx / network) was reported
  as a definitive "❌ 不在 AUR" / "❌ 未打包"; it now distinguishes a real 404 from a transient failure
  (⚠️ 查询失败) via a typed `httpStatusError`.
- **/bbs**: a pathologically long query made a DuckDuckGo button URL exceed Telegram's limit and sink
  the whole reply (including the Arch CN hits already fetched); the button query is now capped and the
  send falls back to text-only if the buttons still fail.
- **/news**: a legitimately empty news page was never cached, so `/news` re-hit upstream on every call;
  freshness is now gated on the fetch time, so an empty fetch is cached like any other.

## [3.7.5] - 2026-06-23

### Fixed
Whole-codebase audit follow-up (the audit found 0 critical issues — the non-feed modules are
"fundamentally sound" like the feed; these are the two concrete defects it surfaced):
- **Verification: a timeout timer firing during graceful shutdown could wrongly decline — and, at the
  strike threshold, auto-ban — a user still mid-verification.** The per-pending timeout
  `time.AfterFunc` runs on `context.Background()` (independent of the SIGTERM context) and shutdown
  didn't stop them. `stopForShutdown` now flags shutting-down (so `consumeNonce` refuses) and stops
  every pending timer before the final save, so in-progress verifications persist intact across the
  restart (the documented guarantee).
- **Config: `timeout_seconds` is now floored at 30** so a typo like `timeout_seconds: 1` can't create
  an unwinnable challenge that strikes real users (mirrors the existing ≤0→240 and >1800→1800 clamps).

## [3.7.4] - 2026-06-23

### Changed / Fixed
Feed reliability & sustainability pass (addresses a multi-agent review of the feed; two adversarial
review rounds, 0 ship-blockers, 24 feed tests under `-race`):
- **Edits paced + capped** — `refreshTracked` does at most 20 edits/cycle, each paced (cancellable, so
  shutdown isn't held up), and stops the cycle on a Telegram 429. A large backlog (e.g. a mass re-mark)
  now drains over several cycles instead of bursting past the rate limit.
- **Resolved bugs stay tracked** so a later reopen/re-resolution re-renders the marker; eviction drops
  the lowest-id RESOLVED bug before any open one, so a long-lived open bug isn't lost before its
  resolution edit. Born-resolved bugs are tracked too.
- **No silent losses** — a tracked bug gone from Bugzilla for 10 whole-fetch cycles, or failing a
  non-rate-limit edit 10 times, is dropped (can't wedge a slot); the newest-bugs window is 100 (was 30)
  with a WARNING if a one-interval burst still exceeds it; the news re-baseline and cursor baseline now
  log; a corrupt state file is backed up to `.corrupt` instead of silently re-baselining.
- **Durability** — state writes are `fsync`'d (file + dir); `fetchBugsByID` is chunked and a failed
  chunk no longer ages out live bugs; the feed flushes its state on shutdown (main waits up to 5s).
- Internal: the closed-bug marker is threaded as a parameter instead of a fragile `🐞`→mark replace.

## [3.7.3] - 2026-06-23

### Changed
- **Bug feed: the closed-bug marker now reflects the resolution — ✅ only for FIXED, ❌ for anything
  closed without a fix** (INVALID/误报, WONTFIX, DUPLICATE, WORKSFORME, OBSOLETE, …). Previously every
  resolved bug became ✅, which misrepresented e.g. a RESOLVED/INVALID bug as "fixed". Applies to both
  a born-resolved bug and a tracked bug's resolution edit. (`resolvedMark`.)

## [3.7.2] - 2026-06-23

### Fixed
- **Bug feed: a bug that is already resolved the first time the feed sees it now renders with ✅ and
  posts silently, instead of the 🐞 "open bug" marker (and a ping).** A bug filed *and* closed within
  one poll cycle (e.g. resolved INVALID, like #977918) was shown as a fresh open bug; it now uses the
  resolved formatting and doesn't notify — it isn't an actionable new open bug. (`formatNewBug`.)

## [3.7.1] - 2026-06-22

### Docs
- Documented **`/spoiler`** where it was missing: the `/help` admin section, README.md,
  README.zh-CN.md, and the state-persistence tables (it persists in `settings.json` alongside
  `/start` `/stop`).

### CI
- Added a **release workflow**: a `vX.Y.Z` tag now builds static linux **amd64 + arm64** binaries
  (version baked in via ldflags), generates `SHA256SUMS`, and attaches them to the GitHub release.

## [3.7.0] - 2026-06-22

### Added
- **`/spoiler` — hide new members' names behind a spoiler (anti-advert).** Spam accounts often set
  their *display name* to an advert, which then shows in the in-group verification challenge. The
  challenge now hides each joiner's name behind a Telegram `<tg-spoiler>` **by default** (one tap to
  reveal), so an ad can't be broadcast just by applying to join. Admin-toggleable via `/spoiler` and
  **persisted** across restart (same `*bool` settings pattern as `/start` `/stop`). Rendered as a
  single HTML-escaped spoiler entity (no nested mention link) so it can never produce an HTML parse
  error that would break the critical challenge post.

### Reliability
- The early-cooldown `DeclineChatJoinRequest` error is now logged (was swallowed) for diagnosability.
- The whole join-verification API-call path was adversarially audited (4 reviewers + synthesis,
  independently re-running build/vet/test/`-race`): **reliable, no must-fix** — fallbacks (兜底)
  confirmed in every failure direction (challenge-post / approve / ban / restart all fail safe; the
  applicant is never silently stranded).

## [3.6.7] - 2026-06-22

### Performance / UX
- **The admin verification buttons (👮 直接通过 / 🚫 举报并封禁, and the "我已关注,继续" recheck) now
  answer the Telegram callback immediately, before the decline/ban/approve/quiz-send round-trips** —
  so the inline button no longer spins ~2 s (it used to stack ~4 sequential ~0.5 s API calls before
  acking). `approve`/`banApplicant` were split into claim/consume + execute so the callback can be
  acked in between; behaviour is unchanged (same race-safe claim-before-network and reopen-on-failure).
- **Confirmed admin status is cached for 60 s** (only admins are cached; the map is bounded + pruned),
  so the admin buttons and `/ban` `/sb` `/warn` skip a ~0.5 s GetChatMember round-trip on repeat use.

### Reliability
- Since the buttons now ack optimistically, a *rare* approve/ban failure is surfaced via a new
  `failAlert`: it posts to the admin-log chat when configured, **otherwise to the group itself** — so
  a failure is never invisible when `admin_log_chat_id` is unset (it is `0` on the live deploy).

## [3.6.6] - 2026-06-21

### Fixed
- **`/pkgs` now takes each distro's version from its NEWEST supported release, not the highest
  version across releases.** An old real package lingering in an older still-supported release was
  masking the newer release's actual one — e.g. Ubuntu `chromium` showed `85.0.4183.83 (22.04 LTS)`
  (a 2020-era deb 22.04 still carries) while 24.04+ ship chromium as a Snap; it now correctly shows
  `snap (26.04 LTS)`. The same fix surfaces the newest openSUSE Leap (16.0) over an older 15.6 build.
  Rolling/dev channels (Debian sid, Fedora rawhide) and the EOL/unreleased exclusions are unchanged.

## [3.6.5] - 2026-06-21

### Docs / governance
- **gosec triage documented** in SECURITY.md (the accepted G304/G703/G706 operator-path & log-taint
  classes, with rationale), giving CI's gosec gate a written baseline. README + SECURITY now state
  that `$STATE_DIRECTORY` must be private to the bot's service user.
- Added GitHub **issue and PR templates** (the PR template encodes the gofmt/vet/test/CI checklist).

### Internal
- Unit test for `missingModRights` (the startup rights preflight), which now also logs when it
  can't read a group's exact rights. The feed confirm-ping reply test asserts
  `AllowSendingWithoutReply`. Coverage 28.3% → 28.7%.

## [3.6.4] - 2026-06-21

### Added
- **Startup permission preflight.** For each guarded group where the bot is an admin, it now logs
  any *missing* moderation right (approve members / ban / delete messages), so a half-granted
  deployment is visible at startup instead of only when an action later fails.
- **CI security gate + dependency automation.** CI now runs `gosec` (with the accepted
  operator-path/log-taint finding classes excluded and documented, so it gates on anything new), and
  a Dependabot config keeps the Go module and GitHub Actions current.

### Internal
- Direct `confirmNotice` wording tests (status-accurate, raw-status fallback) and an
  injectable-fetcher `ensureReleaseInfo` test proving an empty/malformed CSV neither overwrites good
  cache nor earns full-TTL freshness. Coverage 27.7% → 28.3%.
  gofmt / vet / staticcheck / -race / govulncheck / gosec all clean.

## [3.6.3] - 2026-06-21

### Changed
- **Feed confirm notice now replies to the original bug post.** When a tracked bug leaves
  UNCONFIRMED, the 🔔 notice is sent as a Telegram *reply* to that bug's original feed message
  rather than a disconnected new message — so it stays linked to the original (a tap jumps straight
  to it) while still delivering the notification. `AllowSendingWithoutReply` keeps it working if the
  original was deleted. (Resolved bugs remain a silent in-place edit with no ping; new bug/news
  posts are unaffected.)

## [3.6.2] - 2026-06-21

### Internal
- **Verification handler test seam (the long-recommended P2 work, as a focused slice).** Introduced
  a small `verifyBot` interface and widened the approve / decline / ban path (`approve`, `decline`,
  `banApplicant`, `applyBan`, `deleteChallenge`, `adminAlert`) from `*telego.Bot` to it — a
  compile-checked type-widening with **no behaviour change** (`*telego.Bot` satisfies it), so the
  most critical handlers can finally be unit-tested with a fake bot. New tests cover approve
  success, the failed-approve reopen path (the v3.6.1 race guarantee), decline below threshold,
  auto-ban at the strike threshold, and admin report-and-ban. Statement coverage 25.1% → 27.7%.

### Fixed
- **Feed status-notice wording.** A bug leaving UNCONFIRMED straight for IN_PROGRESS was labelled
  "confirmed"; the 🔔 notice now names the bug's *actual* new status (CONFIRMED / IN_PROGRESS / …).
- Removed a redundant deferred `bh.Stop()` (the graceful-shutdown path already stops handlers
  explicitly), and handled the previously-ignored non-200 `resp.Body.Close()` error (gosec G104).

### Notes
- The remaining gosec findings are all the accepted operator-controlled-path / log-taint class under
  the private systemd `StateDirectory=`; documented as accepted rather than annotated inline.

## [3.6.1] - 2026-06-21

### Fixed
- **Verification approve/timeout race — could strike or auto-ban a user who just passed.** A correct
  answer landing right at the verification deadline could race the pending's own timeout timer:
  `approve()` peeked the pending and called Telegram while the timer was still armed, so the timer
  could fire `decline()` — recording a persisted failure strike, declining, and at the strike
  threshold auto-banning a member who had in fact just verified. `approve()` now **claims** the
  pending (stops the timer + marks it done) atomically before the network call, and re-opens it as
  retryable only if the approve fails — so a verified user can never be struck or banned by their
  own timeout. (Found by an internal multi-dimensional audit; the earlier reviews missed it.)
- **Feed confirm ping lost when a bug raced past CONFIRMED.** A silently-posted UNCONFIRMED bug that
  moved straight to IN_PROGRESS (or past CONFIRMED while the ping send transiently failed) never got
  its 🔔 notice. The ping now fires on any UNCONFIRMED → (non-UNCONFIRMED, non-resolved) transition,
  not only exactly CONFIRMED.
- **Release-info: a malformed HTTP-200 distro-info CSV is no longer cached as success for 24h.** An
  empty/garbage 200 that parses to zero rows is treated as a failed fetch (short retry window), so it
  can't overwrite good data or silently disable Ubuntu EOL/dev-series filtering for a day.
- **`settings.json` tolerates a missing field.** `enabled` is now a `*bool`, so a hand-written `{}`
  keeps the seeded default instead of silently pausing verification.

### Internal
- Startup sweeps leftover `.*.tmp-*` state temp files orphaned by a prior hard kill; graceful
  shutdown flushes pending/verifyfail state after handlers stop. New tests for the approve-claim
  invariant, the reopen path, the IN_PROGRESS confirm ping, and the empty-settings default.

## [3.6.0] - 2026-06-21

### Added
- **`/bc allow|deny` accepts both channel-id forms.** The full Bot API form (`-1001234567890`) and
  the bare internal id (`1234567890`, e.g. copied from a `t.me/c/<id>/…` link without the `-100`
  prefix) both work now — the bare form is normalised to the canonical `-100…` id.
- **Verification pause (`/start` · `/stop`) now persists** across restarts via a small
  `settings.json` under `STATE_DIRECTORY`, so a `/stop` during maintenance is no longer silently
  undone by a service restart. (The other runtime toggles — `/rich`, `/autodel`, `/bantime` — still
  reset to config on restart, as documented in the persistence matrix.)

### Fixed
- **`/bc allow` reports an unban failure honestly** instead of always claiming the channel was
  un-banned — the whitelist update still happens, but a failed `UnbanChatSenderChat` is logged and
  surfaced (matching the bot's other honest-feedback paths).
- **Release-info no longer caches a failed fetch as fresh for 24h.** `ensureReleaseInfo` treats the
  Debian/Ubuntu distro-info-data as fresh for the full day only when both fetches succeed; if a
  source fails it retries within ~10 min, so `/pkgs`/`/armpkgs` EOL/dev labels can't silently
  degrade for a whole day after a transient cold-start network blip.
- **`/unmute` no longer silently over-grants** in a restrictive group: if it can't read the group's
  default permissions (`GetChat` failed) it still lifts the mute with a generic allow but says so,
  so an admin can double-check the member's permissions rather than assume the group default was
  restored.

### Internal
- New unit tests for `/bc` id parsing, the release-info freshness window, settings save/load, and
  the rich USE_EXPAND render (previously 0% covered). Coverage 23.2% → 25.0%.
  `gofmt` / `vet` / `staticcheck` / `-race` / `govulncheck` all clean.

## [3.5.0] - 2026-06-21

### Added
- **`/use` now shows USE_EXPAND flags.** Grouped variables that packages.gentoo.org exposes
  separately from local/global USE (e.g. `l10n`, `llvm_slot`) — previously omitted — are now
  rendered: compact and truncated in plain text (a 100+-value group like `l10n` is capped with a
  `…(共 N)` tail), and full inside a collapsible `<details>` block in rich mode.
- **Feed confirm notification.** When a silently-posted **UNCONFIRMED** bug becomes **CONFIRMED**,
  the feed still edits the original message in place *and* sends a one-off non-silent 🔔 notice —
  the notification the silent original never produced. Suppressed when `silent_bugs` is set.
- **Startup feed permission probe.** Before the first poll the bot checks it can actually post in
  each feed target chat (channel admin + post right, or group membership) and logs loudly if not,
  so a misconfigured `chat_id` or a missing right fails visibly at startup instead of at first send.

### Fixed
- **`/pkgs` no longer presents an ancient EOL release as a distro's current version.** Current
  Ubuntu ships apps like Chromium/Firefox as Snap transitional debs (`1snap1`), so the old
  "highest version wins" rule surfaced the last real deb from an end-of-life LTS (e.g. Chromium
  `112` from 18.04). Snap transitional versions are now recognised (shown as `snap`) and EOL
  releases are excluded using the live `eol` dates from distro-info-data — so Chromium/Firefox read
  `snap (26.04 LTS)` while a normal package like `vim` correctly shows `9.1.2141 (26.04 LTS)`.
  Nothing is hardcoded; release roles follow distro-info-data and update automatically.
- **`/armpkgs` no longer presents an unreleased Ubuntu development series as current.** The madison
  path skips a not-yet-released dev series (e.g. `stonking`) in favour of the newest released one,
  flags it `(开发版)` when it's the only series shipping the package, and renders a Snap
  transitional deb as `snap`.
- **`/wiki` de-duplicates case-insensitively**, so capitalization variants of one topic (`NVIDIA`
  vs `NVidia`) collapse to a single result (the simplified-Chinese page still preferred).

### Changed
- **Feed state load-time migration.** A pre-v3.4.3 state file that stored only a bug `status` is
  folded into the current `status|resolution` state key on load, so the first poll after an upgrade
  no longer fires a needless edit for every tracked bug.

### Docs
- README / README.zh-CN now document the full **state-persistence matrix** (what survives a restart
  under `StateDirectory=` vs what is in-memory only) and the feed confirm-notification semantics.

### Internal
- A small `feedBot` interface seams the feed's send/edit calls so `refreshTracked`'s success /
  not-modified / permanent-drop / transient-retry / confirm-ping branches and feed-state save→load
  are now unit-tested with a fake bot (the review's biggest test gap). New tests also cover the
  Snap/EOL `/pkgs` selection, `/armpkgs` suite picking, USE_EXPAND rendering, and `/wiki` dedupe.
  Statement coverage 18.4% → 23.1%. `gofmt` / `vet` / `staticcheck` / `-race` / `govulncheck` clean.

## [3.4.3] - 2026-06-21

### Changed
- **Bug feed edits on ANY state change, not just resolution.** A tracked bug's message is now
  re-rendered whenever its status *or* resolution changes — so an **UNCONFIRMED** bug (which
  posts silently) that later becomes **CONFIRMED** / IN_PROGRESS has its message updated in place,
  and on resolution still swaps 🐞→✅ and stops tracking. Tracking keys on `status|resolution`;
  a redundant "message is not modified" edit is treated as success. Per feed, in each feed's
  own language (EN + ZH both update).

## [3.4.2] - 2026-06-21

Hardening from a fourth external review (no P0/P1 found; all prior fixes re-verified). P3 polish:

### Fixed
- **Honest moderation feedback** when a Telegram call fails: `/bc` no longer claims a channel
  was banned if `BanChatSenderChat` failed (says deleted-but-ban-failed); `/warn`'s limit-kick
  reports "ban succeeded but unban failed" instead of "re-joinable" when the unban errors.
- **`/ban` applies the ban before deleting** the replied message (like `/mute`) — a permission
  failure no longer deletes the offending message while leaving the user un-banned.
- **`/unmute`** restores the **group's default permissions** (via GetChat) instead of a blanket
  allow, so a restrictive group isn't over-granted.
- The fail-open admin alert now states the current mode (fail-open vs fail-closed).

### Hardened
- Quiz pick/shuffle use **crypto/rand** (Fisher–Yates) instead of `math/rand` — it's an
  anti-automation control, and this closes the `gosec` G404 finding.
- A **global outbound HTTP semaphore** (24 concurrent) bounds worst-case network/goroutine
  pressure under group spam, **without** per-user group rate-limiting (keeps "群里不限次").

### Internal
- CI now runs **staticcheck**. Docs say **Go 1.26.4+** (was 1.25+, drifted after the toolchain
  bump). Documented the anonymous-admin and multi-group-different-channel limitations.

## [3.4.1] - 2026-06-21

### Changed
- Tidied the `/help` text and the Telegram slash-command **menu** to be short and accurate
  (the menu truncates long descriptions): dropped the `= /distro` note and the long inline
  distro lists, grouped the admin commands, and shortened every menu entry. `/distro` stays a
  working (unadvertised) alias of `/pkgs`.

## [3.4.0] - 2026-06-21

Mute/unmute + bug-feed resolved-edits, plus two more full-repo adversarial reviews (each
finding verified) — a HIGH verification race and a batch of robustness fixes.

### Added
- **`/mute` / `/unmute`** (admins, reply to a message) — **timed mute (禁言)**: the user stays
  in the group but can't post. No-arg uses `mute_seconds` (default **1h**); an inline duration
  overrides it (`/mute 30m`, `/mute 12h`). Always timed (Telegram auto-lifts on expiry);
  `/unmute` lifts it early. (No permanent mute by design.)
- **Bug-feed `#RESOLVED` edits** — when a posted bug is later resolved/closed, the feed **edits
  its message in place** (🐞→✅, status updated), independently per feed and **in each feed's own
  language** (so the EN and ZH bug channels both update).

### Changed
- **`/sb` vs `/ban` now differ**: `/sb` = 举报并封禁 — **deletes all of the user's messages**
  (spam cleanup) + bans; `/ban` = ban — deletes only the replied message + bans.
- Per-feed **`interval_seconds` is now honoured** — each feed posts on its own interval (the
  shared fetch still runs once per due cycle), instead of every feed following the global minimum.

### Fixed
- **HIGH — stale verification timeout race.** A timed-out attempt's timer could `consume` a
  *freshly re-issued* pending under the same user (it keyed only on group+user), silently
  declining + striking + potentially auto-banning a legitimate re-applicant. Decline is now
  **identity-checked by a per-pending nonce** (`consumeNonce`), and a replaced pending is marked
  done.
- Config-supplied **`ban_seconds` / `mute_seconds` are clamped** to Telegram's honoured window
  (`<30s`→30s, `>366d`→permanent/cap), like the runtime paths — so a config value can't silently
  become a permanent ban/mute reported as finite.
- **Feed robustness:** a corrupt state file (null tracked entry) no longer crashes the bot (skip
  + the poll loop now `recover`s); a permanently-uneditable resolved message is dropped instead
  of retried forever; `flattenAtoms`/summary/keywords truncate by **rune** (no invalid UTF-8) and
  are length-capped so a pathological bug can't wedge the feed; dropped two over-fetched Bugzilla
  fields the decoder ignored.
- `/mute` now applies the restriction **before** deleting the offending message (a permission
  failure no longer deletes a message while leaving the user unmuted).
- `/bug` "not found" reply is now reply-linked + auto-deleted like every other lookup (was the
  lone path that lingered).
- `ensureReleaseInfo` clears its in-flight flag via `defer` (a panic can't freeze `/pkgs` labels).

### Internal
- `moderate()` now reuses the shared `warnPrecheck` (admin-gate + reply-target + skip-admins),
  so all six reply-target moderation commands share one precheck. README `/mutetime` references
  corrected to the inline form; zh-CN privacy-mode setup note clarified (only `/bc` needs it off).
  Added tests for the nonce identity-check, the duration clamps, and rune-safe truncation.

## [3.3.0] - 2026-06-21

Configurable ban duration + verification anti-spam, plus fixes from an external code review
and a follow-up adversarial review of the new code (13 confirmed findings, each verified).

### ⚠️ Upgrade note
- **`/sb` is now a ban, not a re-joinable kick.** Both `/sb` and `/ban` now ban for the
  configured duration (`/bantime`, **default permanent**). If you relied on `/sb` being a
  soft kick, set `ban_seconds` to a finite value (e.g. `3600`) or use `/warn` for lenient
  moderation.

### Added
- **Configurable ban duration** — `/bantime` (admins): `0`=permanent (default), or `7d`/`12h`/
  `30m`/`3600`. Used by `/ban`, `/sb`, the verification auto-ban and the report button. Config
  `ban_seconds`. Durations are clamped to Telegram's honoured window (under 30 s → 30 s, over
  366 days → permanent) so the reported duration always matches what Telegram enforces.
- **Verification anti-spam** — a failed verification declines with a `verify_retry_seconds`
  (default 180) cooldown before re-applying; after `verify_max_fails` (default 3) failures
  **within a rolling window** the applicant is auto-banned for the configured duration. Strikes
  persist across restarts, reset on success, and **age out** so a genuine user's isolated
  mistakes don't accumulate. Negative values disable the cooldown / auto-ban.
- `required_channel_fail_open` — keep the default fail-open (a channel-permission slip won't
  lock everyone out) or set `false` to strictly enforce the channel gate.

### Fixed
- **Build toolchain** raised to **Go 1.26.4** (`go.mod` was `1.25.7`, below the fix for two
  stdlib advisories — `net/textproto` GO-2026-5039 and `crypto/x509` GO-2026-5037); **CI now
  runs `govulncheck`**. (The deployed binary was already built with 1.26.4.)
- **Stale DM quiz buttons** can no longer answer a *new* verification: each pending carries a
  random **nonce** in its callback data (legacy 3-part buttons still work across the upgrade).
- **`/ban` report button** reports honestly — a failed ban no longer claims success.
- **Verification auto-ban** only clears an applicant's strikes when the ban **actually
  succeeds**; where the bot lacks ban rights, strikes are kept (no infinite-retry loop) and
  admins keep getting alerted.
- **Data race** in the verification cooldown read fixed (fields copied under the lock).
- **State writes** use a unique temp file + a serialize lock (no shared-`.tmp` clobber under
  concurrent saves).
- Config **fail-fast validation**: rejects group id `0`, duplicate group ids, and malformed
  overlay `repo` / duplicate overlay names. `/news` now logs loudly if it parses 0 items
  (page-layout drift) instead of silently returning empty.

## [3.2.0] - 2026-06-20

Auto-delete consistency pass + a third multi-dimension audit (7 fresh dimensions, each
finding adversarially verified): 23 raised, 13 confirmed (0 critical/high), all fixed below.

### Changed
- **Auto-delete is now consistent across a lookup's whole interaction.** A lookup command's
  usage hint / "not found" / disambiguation reply previously used a path that left the command
  un-deleted and could fail to render; it now replies (reply-linked) and the command + reply
  are removed together after `lookup_ttl`, exactly like a successful answer. Control/admin
  commands keep deleting their trigger immediately; auto-delete still never runs in a DM.

### Fixed
- **`/pkgs` no longer shows a bare-date snapshot instead of the real version.** A package a
  distro ships as a bare 8-digit `YYYYMMDD` (e.g. Debian's `gcc-snapshot`) was treated as a
  huge real version and beat the actual release; it's now recognised as a date and ranked
  below real releases (gcc Debian shows `16.1.0` / `14.2.0`, not the snapshot date).
- **`/pkgs` Ubuntu line shows the current released release, not an in-development one.** An
  unreleased Ubuntu series (e.g. `26.10` before its release date) and `proposed`/`backports`
  pockets are now excluded from the stable line (derived live from distro-info-data release
  dates, mirroring Debian) — so it shows e.g. `26.04 LTS`, not `26.10`.
- **`private_reply`** (admin-supplied DM auto-reply, sent in HTML mode) now falls back to
  plain text and logs the error if a stray `<`/`>`/`&` makes Telegram reject it — so a typo
  in the config can't leave DMs silently unanswered.

### Hardened
- The private-chat query rate-limit map (`queryHits`) now has a hard upper bound (wholesale
  clear, like `dmLast`) instead of only soft eviction — flood-proof under a pathological burst.
- `ensureReleaseInfo` has an in-flight guard so a burst of `/pkgs` on a cold/expired cache
  triggers one upstream distro-info fetch, not N.

### Internal
- Factored the duplicated lookup send+reply+cleanup tail into `replyLookupHTML`/`replyLookupPlain`
  (used across all lookup handlers); factored `/autodel`'s argument parsing into a pure,
  unit-tested `parseAutoDelArg`. Added tests for the bare-date detection, the Ubuntu exclusion,
  and the `/autodel` parser. Fixed stale command lists (the `/autodel off` message, the
  `lookup_ttl_seconds` docs and the `/armpkgs` help/menu omitted `/arm`·`/armpkgs`·AUR) and a
  `/pkgs` "not found" message that under-listed distro families. Removed audit scratch files;
  `dm_test.go` uses `context.TODO()` (staticcheck-clean).

## [3.1.3] - 2026-06-20

### Fixed
- **`/help` in a private chat now actually outputs.** Its DM reply was sent in HTML parse
  mode, but the help text contains literal `<包名>` / `<编号>` placeholders that Telegram
  rejects as malformed HTML ("can't parse entities") — and the send error was discarded, so
  nothing appeared. It (and `/ping` / `/stats` in DM) now send as plain text, matching the
  group path. Verified against the live Bot API.

## [3.1.2] - 2026-06-20

### Fixed
- The per-minute private-chat rate limit now applies **only to the commands that make an
  external request** (`/pkg` `/use` `/bug` `/news` `/wiki` `/bbs` `/pkgs` `/arm` `/armpkgs`).
  The cheap local commands `/help`, `/ping`, `/stats` are no longer counted against it, so
  they always respond in a DM (previously `/help` could be blocked after a few queries).

## [3.1.1] - 2026-06-20

### Fixed
- `/help`, `/ping` and `/stats` now also work **in a private chat** (they previously got the
  generic auto-reply). And `/ping` / `/stats` — which are in the member command menu — are
  now usable by any member, not just admins (they were incorrectly admin-gated). All member
  commands are now consistent: usable in a guarded group (unlimited) and in a DM (rate-limited);
  admin/moderation commands stay group-only.

## [3.1.0] - 2026-06-20

### Added
- **Lookup commands now work in a private chat** with the bot (`/pkg` `/use` `/bug` `/news`
  `/wiki` `/bbs` `/pkgs` `/arm` `/armpkgs`), **rate-limited per user** to
  `private_query_per_min` queries/minute (config, default 3) to prevent abuse — guarded
  groups stay unlimited. Other DMs still get the unified auto-reply (now updated to mention
  this). Auto-delete doesn't apply in DMs (nothing to keep tidy there).

## [3.0.0] - 2026-06-20

A milestone release: the cross-distro `/pkgs` channel logic is now centred on each distro's
**current stable release**, with the rolling/dev channel shown above it when ahead.

### Changed
- `/pkgs` now centres on the **current stable release** and adds the rolling/dev channel
  above it only when that's newer — so Fedora shows its stable (`44`) even when rawhide
  matches it (was: `(rawhide)`), and a package whose stable lags shows both (rawhide + 44).
- **Debian's "stable" excludes the testing series** (forky/14 today) using the live
  distro-info-data status, so the stable line is the real stable (`13`/trixie), e.g.
  `nano 9.0 (unstable/sid)` + `8.4 (13 stable)` instead of mistaking testing for stable.
- The Debian unstable channel is labelled **`unstable/sid`** (it's codenamed sid, which many
  people track) so it's recognisable.

### Notes
- Kept the flat single-`package main` layout (22 source files, by-command) rather than
  splitting into sub-packages — see CONTRIBUTING "Project layout" for the rationale.

## [2.7.0] - 2026-06-20

### Added
- `/pkgs` labels Debian/Ubuntu releases by their **live role** — `stable` / `testing` /
  `oldstable` / `LTS` — derived from Debian's `distro-info-data` (release dates), not
  hardcoded; so when Debian 14 releases, "stable" follows it with no code change. (Debian
  firefox now reads `140.12.0 (13 stable)`.)
- The **RHEL ecosystem is split into three distinct families**: **RHEL** (the AlmaLinux/Rocky
  1:1 rebuilds — the actual RHEL versions), **CentOS Stream** (the rolling upstream of the
  next RHEL), and **EPEL** — each with its own version, since they are different products.
  (firefox now shows `RHEL 140.11.0 (9)` separately from `CentOS Stream 140.11.0 (10)`.)

## [2.6.1] - 2026-06-20

### Fixed
- `/pkgs` labels each version by the **newest release that actually ships it**, not whichever
  repo was scanned first — so e.g. RHEL/EPEL firefox `140.11.0`, present in AlmaLinux 8/9 and
  CentOS Stream 9/10, now shows `(stream.10)` instead of the misleading `(8)`.

## [2.6.0] - 2026-06-20

Hardening from a second full multi-dimension audit (each finding adversarially verified).

### Fixed
- **Version comparator**: a Gentoo pre-release (`1.0_rc1`, `_alpha`, `_beta`, `_pre`) is now
  correctly older than the release, and a patch `_pN` / revision `-rN` newer — previously any
  extra suffix sorted as *newer*, so `/pkg`/`/distro` could show an rc as "latest".
- **`commandArg`** splits on the first run of whitespace, so a tab/newline-separated argument
  (a pasted `/pkg<newline>vim`) works, not just a single space.
- **Feed poll interval** now clamps a too-fast `interval_seconds` (1–59) to the 60 s floor
  instead of silently falling back to 5 minutes.

### Changed / Hardened
- Lookup commands root their HTTP timeout on the request context, so in-flight work is
  cancelled on shutdown instead of lingering ~20 s.
- Persistence writes now go through one atomic helper that **logs** marshal/write/rename
  failures; the bot `MkdirAll`s `STATE_DIRECTORY` on start; the warn/antispam/feed loaders
  log a corrupt-file parse error (matching `pending.json`) instead of silently dropping state.
- Duplicate feed targets (same `chat_id`) are de-duplicated at config load (they would have
  shared one cursor and dropped each other's items).
- **CI now runs `go test -race ./...`** (the test suite previously wasn't gating merges).
- The build embeds a `version` (via `-ldflags -X main.version`), shown in `/ping` and the
  startup log.

## [2.5.3] - 2026-06-20

### Changed
- `/pkgs` shows a distro's **rolling/dev channel AND its current stable** on separate lines
  when they differ (e.g. Debian `152.0.1 (unstable)` + `140.12.0 (13)`), like the Gentoo
  amd64/~amd64 split — instead of only the newest. The stable is labelled by the highest
  release that actually ships it (Debian → `13`/trixie, not `14`/forky), and a package at one
  version everywhere stays a single line (labelled by its rolling channel when applicable).

## [2.5.2] - 2026-06-20

### Changed
- `/armpkgs` output is tidier: each distro is a **clickable link** to its package page, and
  Debian/Ubuntu show just the newest arm64 channel (not three) with a shorter footer.
- `/pkgs` Debian/Ubuntu links now point to the **clean package pages** (tracker.debian.org,
  Launchpad) instead of the cluttered `packages.debian.org` / `packages.ubuntu.com` search.

## [2.5.1] - 2026-06-20

### Internal
- Renamed `distro.go` → `pkgs.go` and the handler `onDistro` → `onPkgs` so the file
  matches the now-primary `/pkgs` command (`/distro` stays an alias). No behaviour change.

## [2.5.0] - 2026-06-20

### Added
- `/armpkgs` now also checks **AUR** (from the PKGBUILD `arch=()` declaration: any /
  aarch64 / 32-bit-ARM-only / x86-only).

### Fixed
- `/distro` no longer mis-attributes a different distro's repo to a family when the repo
  id merely shares a prefix (e.g. Arch Linux POWER's `archpower_*` was being counted as
  "Arch"); matching now requires an exact id or a `<prefix>_<release>` form.

## [2.4.0] - 2026-06-20

### Added
- **`/armpkgs <pkg>`** — cross-distro arm64 (aarch64) support check across **Gentoo,
  Debian, Ubuntu, Fedora and Arch Linux ARM**, each via its own per-architecture API
  (Debian/Ubuntu madison arch-filtered, Fedora mdapi, ALARM package presence), queried
  concurrently. Built for the case where Gentoo hasn't keyworded a package for arm64 but
  other distros ship it.
- **`/pkgs`** as a memorable alias for `/distro` (cross-distro version search).

## [2.3.0] - 2026-06-20

### Changed
- `/distro` now annotates each distro's version with the **release it comes from** —
  e.g. `Debian 152.0.1 (unstable)`, `Fedora 152.0 (43)`, `Alpine … (edge)`,
  `openSUSE Leap … (15.6)` — so you can tell whether a version is from a rolling/unstable
  branch or an older stable release. Stays one line per distro (no per-release wall).

## [2.2.0] - 2026-06-20

### Added
- **`/arm <pkg>`** — show a Gentoo package's **arm64 (aarch64) keyword status** (stable
  version, newest `~arm64` testing version, or "not keyworded"), so ARM users can quickly
  see whether a package is available/tested for their arch.

## [2.1.0] - 2026-06-20

### Added
- **Auto-delete for lookup results** — a lookup command (`/pkg` `/use` `/bug` `/news`
  `/wiki` `/bbs` `/distro`) and its answer are removed after a delay (default **3 minutes**)
  to keep the group tidy. Configurable via `lookup_ttl_seconds` (unset → on at 3 min;
  `0`/negative → off) and adjustable at runtime by admins with **`/autodel`**
  (`/autodel on|off` or `/autodel <minutes>`).

## [2.0.0] - 2026-06-20

Stable release after a full multi-dimension audit (logic, APIs, concurrency/leaks,
docs, permissions, robustness), each finding adversarially verified and the confirmed
ones fixed and unit-tested.

### Added
- Lookup commands (`/distro` `/pkg` `/use` `/bug` `/news` `/wiki` `/bbs`) now **reply to
  the command message**, so concurrent answers (these hit slow external APIs) can't be
  mistaken for one another.
- A unit-test suite (`*_test.go`) covering the version comparator, per-group config
  resolution/validation, feed cursor logic, status-aware notifications and the quiz shuffler.
- Startup now probes each required channel and logs whether the bot can read its members.

### Fixed
- **Feed news dedup** no longer re-broadcasts the entire news archive if the stored cursor
  URL falls out of the fetched list — it re-baselines instead of flooding the channel.
- **Feed bug cursor** is now strictly forward-only (can't regress and re-post older bugs).
- **Version comparison** (`/pkg`, `/distro`) handles double-digit revisions/suffixes
  correctly (`r10` is newer than `r2`), via overflow-safe natural-order token comparison.
- **Required-channel gate fails open** when the bot can't read the channel (it isn't an
  admin there): rather than silently locking every applicant out, it passes verified users
  through and alerts admins — so a permission slip can't break joining.

### Changed / Hardened
- Bounded the `/pkg` version/info caches; warn when `STATE_DIRECTORY` is unset (persistence
  off) or a feed has `chat_id=0`; the DM auto-reply is throttled per user; failed admin-log
  sends are now logged. `config.example.json` gains `warn_limit` + `silent_bugs`; docs and
  the `/distro` command-menu text brought in sync.

## [1.9.1] - 2026-06-20

### Changed
- `/distro` now shows Gentoo's **amd64-stable and ~amd64 (testing) on separate lines**
  when they differ (from packages.gentoo.org, reusing the `/pkg` version logic) — e.g.
  firefox shows `Gentoo amd64 140.12.0` and `Gentoo ~amd64 152.0` — and collapses to one
  line when stable == testing.

## [1.9.0] - 2026-06-20

### Changed
- `/distro` now also covers **Gentoo and Alpine**, and lists ecosystem variants on
  separate lines (Fedora vs RHEL/EPEL, openSUSE Leap vs Tumbleweed). Per-distro version
  picking now prefers a real release over a date/CalVer over a Gentoo `9999` live ebuild,
  so each family shows its actual packaged version (a date-only project like yt-dlp still
  shows its newest date).

## [1.8.1] - 2026-06-20

### Changed
- `/distro` with no exact match now shows the **closest cross-distro package's full version
  table** (ranked by distro coverage) plus a collapsible (`<details>` in rich) list of other
  matches, instead of a bare list of names — so near-misses and vague queries still return
  real data. (The Linux kernel stays unresolvable cross-distro: each distro names it
  differently and neither Repology nor pkgs.org exposes a clean unified entry.)

## [1.8.0] - 2026-06-20

### Changed
- `/distro` now links **each distro to its package page** (clickable), honors the
  rich-message toggle (`rich_messages` / `/rich`) like `/pkg` and `/use`, and — when
  there's no exact match — suggests the closest packages that **actually exist across
  distros** (ranked by coverage, language-namespaced entries filtered) instead of
  silently picking a wrong or unpackaged project (e.g. `kernel` no longer → `genkernel`).

## [1.7.1] - 2026-06-20

### Added
- On startup the bot logs whether it is an admin in each guarded group, so a group it
  hasn't been granted admin in yet is clearly visible (and confirmed harmless — Telegram
  doesn't deliver join requests there) rather than silently inert.

## [1.7.0] - 2026-06-20

### Added
- **Per-group configuration** — a new `groups` array lets each guarded group set its own
  `required_channel_id`, `channel_display`, `channel_invite_url` and `questions`, each
  falling back to the global default when unset. The legacy `group_ids` / `group_id` are
  still accepted (treated as groups with no overrides). A configured group the bot isn't
  yet an admin of stays inert (no join requests reach it) rather than erroring.

## [1.6.0] - 2026-06-20

### Added
- **`/distro <pkg>`** — cross-distro package version lookup via the Repology API,
  showing the current version in AUR, Arch, Debian, Ubuntu, Nixpkgs, openSUSE and
  Fedora in one message.

## [1.5.4] - 2026-06-20

### Changed
- Feed bug footer is split into separate labelled lines (负责 / 报告 / 日期), and the
  **assignee and reporter now link to their Gentoo Bugzilla bug list** (substring email
  match, since Bugzilla redacts emails for anonymous API access).

## [1.5.3] - 2026-06-20

### Changed
- Feed bug **Priority** and **Severity** are now shown as two separate labelled lines
  (优先级 / 严重性) instead of one combined `重要度` line, so even identical values read
  clearly. Supersedes the 1.5.2 collapse.

## [1.5.2] - 2026-06-20

### Changed
- Feed bug **importance** collapses a redundant priority·severity when both render the
  same word (e.g. `普通 · 普通` → `普通`, `Normal · normal` → `Normal`); distinct
  values like `普通 · 增强` are unchanged.

## [1.5.1] - 2026-06-20

### Changed
- Feed bug notifications are now status-aware: **UNCONFIRMED bugs post silently** (a
  fresh report may be a false alarm) and confirmed bugs post with a notification.
  `silent_bugs: true` still forces every bug silent. (Was: all bugs silent by default.)

## [1.5.0] - 2026-06-20

### Added
- **`/bc`** — admin command to toggle the channel sock-puppet filter on/off at runtime,
  plus `/bc allow|deny <channel id>` to manage its whitelist (`allow` also un-bans the
  channel). The toggle and whitelist are now **persisted** (`antispam.json`), so they
  survive restarts; `block_channel_senders` / `channel_whitelist` seed the initial state.

## [1.4.0] - 2026-06-20

### Added
- **Channel sock-puppet block** (`block_channel_senders`, off by default) — a message
  posted in a guarded group on behalf of a channel is deleted and that channel is
  banned from posting; anonymous group admins and the linked discussion channel are
  exempt, and a `channel_whitelist` allows specific channels. Requires the bot's
  privacy mode to be OFF so it can see these messages.

## [1.3.1] - 2026-06-20

### Fixed
- The DM auto-reply now also covers **commands** sent in a private chat (`/pkg`,
  `/help`, …), which previously matched their group-only handler and silently did
  nothing. The `/start` verification deep link still reaches the verify flow.

## [1.3.0] - 2026-06-20

### Added
- **DM auto-reply** — a direct message to the bot outside the verification flow now
  gets a single unified reply (pointing to the group + commands) instead of silence.
  Customizable via the `private_reply` config key.

## [1.2.1] - 2026-06-20

### Changed
- The Chinese bug feed (and `/bug`) now localizes the Bugzilla **status, resolution,
  severity and priority *values*** to Chinese (e.g. `RESOLVED / FIXED` → 已解决 / 已修复,
  `Normal · normal` → 普通 · 普通), not just the field labels. The English (`lang:en`)
  feed is unchanged. Component names, keywords and people stay as-is.

## [1.2.0] - 2026-06-20

### Added
- **`/bbs <query>`** — Linux forum search. Inline results from the Arch Linux CN
  forum (Chinese, via its Discourse API), plus one-tap site-search buttons for the
  major English forums (Gentoo, Arch BBS, Ubuntu, Debian) — Chinese first, English
  as backup.

## [1.1.1] - 2026-06-20

### Changed
- `/wiki` now shows each page's **Chinese display title** for Gentoo `/zh-cn` pages
  (e.g. "Kernel/Gentoo 内核配置指南" instead of the English "…/zh-cn" title), filters
  out foreign-language pages that aren't tagged as translations, and widens the
  search window to surface more simplified-Chinese pages.
- `/help` and the command menu now show the actual configured `warn_limit` (was a
  literal "N").

## [1.1.0] - 2026-06-20

### Added
- **`/wiki <query>`** — search the Gentoo and Arch wikis (MediaWiki), **preferring
  Simplified-Chinese pages** and falling back to the default page; other languages
  are filtered out.
- **`/warn` and `/clearwarn`** — an admin, reply-based warning system. A user is
  auto-kicked (rejoinable) after `warn_limit` strikes (default 3); counts persist
  across restarts.

## [1.0.1] - 2026-06-20

### Added
- **Auto-leave unauthorized chats** — the bot now leaves any group/channel it is
  added to that isn't a configured chat (a guarded group, the required channel, a
  feed target, or the admin-log chat). Prevents being pulled into arbitrary groups.

## [1.0.0] - 2026-06-20

First stable release.

### Features
- **Join-request verification:** an in-group deep link opens a DM quiz (randomized,
  option-shuffled), with an optional required-channel follow gate (two-step in DM).
  Wrong answer / timeout auto-declines; admins can one-tap approve or report-and-ban.
- **Multiple guarded groups** from a single instance; **restart-safe** — in-progress
  verifications are persisted and their timers re-armed on restart.
- **Moderation:** `/sb` (delete + kick, rejoinable) and `/ban` (delete + permanent
  ban) — admin-only, reply-based, and they refuse to target other admins.
- **Gentoo helpers:** `/pkg` (official tree + overlays, with versions), `/use` (USE
  flags + info), `/bug` (Bugzilla), `/news`. Optional Bot API 10.1 rich output for
  `/pkg` and `/use` (`rich_messages` / `/rich`), off by default.
- **Auto-feed (optional):** polls Gentoo Bugzilla + news and broadcasts new items to
  one or more destinations (`feeds`), each with its own language and filters, from a
  single shared fetch per cycle. Deduped, restart-safe, baselines on first run.
- Single static binary (only dependency: [telego](https://github.com/mymmrac/telego));
  long polling, no inbound port; ships a hardened `systemd` unit (`DynamicUser` +
  sandboxing) and reads its token from the environment.

[3.12.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.12.0
[3.11.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.11.1
[3.11.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.11.0
[3.10.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.10.0
[3.9.3]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.9.3
[3.9.2]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.9.2
[3.9.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.9.1
[3.9.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.9.0
[3.8.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.8.1
[3.8.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.8.0
[3.7.6]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.7.6
[3.7.5]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.7.5
[3.7.4]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.7.4
[3.7.3]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.7.3
[3.7.2]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.7.2
[3.7.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.7.1
[3.7.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.7.0
[3.6.7]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.6.7
[3.6.6]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.6.6
[3.6.5]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.6.5
[3.6.4]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.6.4
[3.6.3]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.6.3
[3.6.2]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.6.2
[3.6.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.6.1
[3.6.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.6.0
[3.5.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.5.0
[3.4.3]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.4.3
[3.4.2]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.4.2
[3.4.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.4.1
[3.4.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.4.0
[3.3.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.3.0
[3.2.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.2.0
[3.1.3]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.1.3
[3.1.2]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.1.2
[3.1.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.1.1
[3.1.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.1.0
[3.0.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.0.0
[2.7.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.7.0
[2.6.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.6.1
[2.6.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.6.0
[2.5.3]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.5.3
[2.5.2]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.5.2
[2.5.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.5.1
[2.5.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.5.0
[2.4.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.4.0
[2.3.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.3.0
[2.2.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.2.0
[2.1.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.1.0
[2.0.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.0.0
[1.9.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.9.1
[1.9.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.9.0
[1.8.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.8.1
[1.8.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.8.0
[1.7.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.7.1
[1.7.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.7.0
[1.6.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.6.0
[1.5.4]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.5.4
[1.5.3]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.5.3
[1.5.2]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.5.2
[1.5.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.5.1
[1.5.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.5.0
[1.4.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.4.0
[1.3.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.3.1
[1.3.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.3.0
[1.2.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.2.1
[1.2.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.2.0
[1.1.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.1.1
[1.1.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.1.0
[1.0.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.0.1
[1.0.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.0.0
