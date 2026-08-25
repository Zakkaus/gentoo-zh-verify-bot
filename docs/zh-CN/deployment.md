# 部署

本文说明首次启动、持久 owner 认领、运行时群组注册、权限诊断和仓库提供的 systemd unit。配置项定义不在此重复。

## 只提供 `BOT_TOKEN` 的首次启动

**实现位置：**`main` 包；`cmd/gentoo-zh-verify-bot/main.go` 中的 `main` 和 `loadRuntimeState`；`internal/config` 包；`internal/config/config.go` 中的 `LoadConfig`；`internal/store` 包；`internal/store/baseline.go` 中的 `LoadBaseline` 和 `EffectiveConfig`。

除 `-version` 外，应用只强制要求 `BOT_TOKEN`。默认配置路径为 `/etc/gentoo-zh-verify-bot/config.json`。文件不存在时按 `{}` 处理，以零个已配置群组启动。现有文件不可读、JSON 损坏、群组、模式、题目、频道或 baseline 无效时，启动失败。未知 key 只记录警告。

`STATE_DIRECTORY` 非空时，`loadRuntimeState` 尝试以 `0700` 创建目录，清理遗留的 `.<name>.tmp-*`，并把 `settings.json` 放在该目录中。创建失败只记录警告，启动继续，之后的持久化可能失败。变量为空时，普通设置只存在于内存，验证、警告和 feed 状态都不能跨重启保存。Owner 认领和运行时群组注册要求更严格：没有持久设置存储时直接拒绝。

随后，程序创建 Bot API 客户端，强制执行 `GetMe`，创建处理器，并先注册 owner 和 enrollment 路由，再注册普通应用路由。处理器启动并确认正在接收 update 后，long polling 才能首次获取 backlog。机器人创建、处理器创建或启动、首次 long polling 或 `GetMe` 失败都会结束进程。程序还会启动异步权限检查，注册命令菜单，并启动可选 feed、查询缓存预热和 heartbeat。更新流在没有停止信号时结束，进程以非零状态退出，交由 systemd 重启。

## Owner 认领

**实现位置：**`main` 包；`cmd/gentoo-zh-verify-bot/registration.go` 中的 `(*registrationService).EnsureOwnerClaim` 和 `(*registrationService).onOwnerClaim`；`internal/store` 包；`internal/store/settings.go` 中的 `(*Settings).EnsureOwnerClaim` 和 `(*Settings).ClaimOwner`。

尚未记录 owner 时，启动过程持久创建或复用一个有效期 24 小时、只能使用一次的 nonce，并在日志中输出私有链接 `https://t.me/<bot>?start=owner_<nonce>`。用户在机器人 DM 中打开有效链接后，其 Telegram 用户 ID 被记录为 owner，nonce 同时失效。

Nonce 缺失、不匹配、已经使用或过期时，认领被拒绝。存储失败时，用户收到保存失败，owner 保持未认领。设置存储不存在、不可读、版本不支持或不可写时，启动日志会说明认领不可用；程序不会创建只存在于内存的 owner。

链接使用前应按秘密 capability 管理。代码除持有有效 nonce 外，不执行第二种身份校验。

## Owner 授权的群组注册

**实现位置：**`main` 包；`cmd/gentoo-zh-verify-bot/registration.go` 中的 `(*registrationService).onEnrollmentCommand`、`(*registrationService).onEnrollmentStart`、`(*registrationService).onMyChatMember`、`(*registrationService).scheduleUnknownLeave` 和 `(*registrationService).registrationCompleted`；`internal/store` 包；`internal/store/settings.go` 中的 `(*Settings).IssueEnrollmentNonce` 和 `(*Settings).CommitRegistrations`。

Owner 在 DM 中执行 `/enroll` 后，机器人持久生成有效期十分钟、只能使用一次的 `startgroup=enroll_<nonce>` 链接。非 owner 收到仅限 owner 的拒绝；持久化失败时收到保存失败。

在目标群组打开链接的人必须是当前人类管理员，且机器人自身成员状态必须可读。机器人已经是 creator 或 administrator 时立即提交注册；只是普通成员时，程序持久记录待注册状态，等待十分钟内完成提升。只有匹配且未过期的待注册记录才能由提升操作完成。

持久 owner 也可以直接添加或提升机器人完成注册。此前没有任何有效群组时，第一个注册群组会成为持久控制群组。注册无效、过期或重复使用，操作者是机器人或非管理员，成员状态不可读，机器人状态不合要求，未经授权提升，或持久化失败时，程序发送拒绝并尝试退出群组。Owner 已存在时，未知群组中普通成员状态的机器人最多等待十分钟，以便收到有效注册 payload；尚未认领 owner 时会立即拒绝。退出失败只记录日志，机器人会暂时保留在群组中。

注册会立即把群组写入 `settings.json`。管理操作、`/settings` 和注册触发的权限报告在同一进程中即可读取。入群验证和部分命令仍使用启动时生成的 `config.Config` 快照，命令菜单也只在启动时安装。注册后应先重启服务，再依赖入群验证或完整命令入口。

注册完成后，`registrationCompleted` 发送 `?start=configure_<group_id>`。设置面板只解析 `panel_<token>` 链接，代码中没有 `configure_` 处理器；该链接会进入普通 DM `/start` 路径并尝试发送申请者题目。因此，代码无法说明该链接原本应执行的设置行为。请在已注册群组中执行 `/settings`，由实际面板入口生成链接。

## 权限自检

**实现位置：**`internal/moderate` 包；`internal/moderate/service.go` 中的 `(*Service).CheckGroupSetup`、`(*Service).LogGroupSetup` 和 `(*Service).LogGroupAdmin`；`internal/feed` 包；`internal/feed/feed.go` 中的 `probeFeedPerms`。

启动过程异步检查每个有效受保护群组。检查内容包括群组可读、机器人是管理员或 owner，以及以下权限：

- 邀请用户，用于批准入群申请；
- 封禁或限制成员，用于封禁、禁言、警告移出和频道身份封禁；
- 删除消息，用于清理和删除管理证据；
- 每个必加频道中的 administrator 或 owner 状态。

已就绪群组只写日志，不发送消息。缺少权限时，程序记录完整报告，并依次尝试发送给运行时注册者、管理日志群组和当前群组；首次发送成功后停止。查询和投递错误会在报告周边记录。自检只用于诊断，不会停用处理器。设置面板没有重新执行自检的按钮。重启会重新检查全部群组；运行时注册完成后会立即检查该群组。

Feed 目标另有非致命启动检查。频道要求机器人是管理员且具有 `can_post_messages`；群组和超级群组要求机器人未退出、未被封禁，并且能够发送消息。检查失败只记录警告，feed 仍会运行。

## systemd 运维与重启恢复

**实现位置：**`main` 包；`cmd/gentoo-zh-verify-bot` 中的 `main`、
`prepareUpdateHandler`、`pollingProgressCaller` 和 `systemdNotifier`；部署定义
`deploy/gentoo-zh-verify-bot.service`。

### 进程监管策略

仓库提供的 unit 执行 `/usr/local/bin/gentoo-zh-verify-bot --config
/etc/gentoo-zh-verify-bot/config.json`，读取 `/etc/gentoo-zh-verify-bot/bot.env`，使用
`DynamicUser=yes`，并以 `0700` 模式创建 `/var/lib/gentoo-zh-verify-bot` 作为
`STATE_DIRECTORY`。

`Restart=always` 覆盖崩溃、watchdog 终止和意外的正常退出。30 秒重启间隔可避免高频崩溃
循环。`StartLimitIntervalSec=0` 会停用 systemd 的启动速率限制，因此持续的启动错误会每
30 秒重试一次，不会在五次失败后永久停止。管理员执行 `systemctl stop` 后，unit 仍会保持
停止。

服务使用 `Type=notify`。程序完成身份查询、状态恢复和处理器注册，并确认处理器正在接收 update
后，才会启动 long polling；`READY=1` 在此之后发送。每次 `getUpdates` 调用结束后发送
`WATCHDOG=1`；独立 ticker 无法掩盖停滞的 poll 循环。无更新时，正常 long poll 会在 30 秒内
结束。每次 API 调用最多执行 45 秒，失败后等待五秒再重试，因此两次进度信号之间最长为 50 秒。
`WatchdogSec=120s` 超过该间隔的两倍。网络、DNS 或 Telegram 故障仍会结束一次调用，所以
watchdog 会继续收到信号，并由重试和验证中断恢复逻辑处理。如果 poll 循环连续 120 秒未结束
任何调用，systemd 会终止进程，并在 30 秒后重启。

SIGINT 或 SIGTERM 会触发 `STOPPING=1`，并先停止 long polling。程序随后处理 Telego buffer 中
已经获取的全部 update；后续 poll 的 offset 可能已在上游确认这些 update，因此不能直接丢弃。
该处理过程、正在执行的 update 处理器、Telegram heartbeat 和 feed 状态写入共用 20 秒截止时间。
随后，程序冻结验证计时器并同步保存验证状态。`TimeoutStopSec=30s` 会在强制终止前额外保留
十秒。停止日志会列出当前等待的组件。

所有状态写入共用一把进程级 mutex。程序在目标目录创建模式为 `0600` 的临时文件，写入并执行
`fsync`，关闭文件，原子重命名为状态文件，再对父目录执行 `fsync`。如果进程在重命名前终止，
原文件仍然完整；启动过程会清理遗留的 `.<name>.tmp-*` 文件。存储错误仍可能使最新的内存变更
未进入最后一次成功写入的快照。

### 重启后的状态

| 状态 | 重启后的行为 |
| --- | --- |
| 待验证申请及其截止时间 | `pending.json` 恢复验证消息、nonce、尝试次数、题目状态和截止时间。长时间中断按下文规则恢复。 |
| 必需频道条件 | `settings.json` 或 `config.json` 保留运行时或静态配置。程序会实时检查频道成员状态，不会信任重启前的检查结果。 |
| 验证失败记录和重试冷却 | `verifyfail.json` 恢复失败次数和最近一次失败时间。 |
| 警告计数 | `warns.json` 恢复每个群组和用户的正数计数。 |
| 禁言到期时间 | Telegram 保存 `until_date`；即使机器人停止运行，服务器也会按时解除限制。程序没有需要重建的本地禁言计时器。 |
| Feed 游标和受跟踪消息 | 每个 `feed-<chat_id>.json` 恢复 Bug 和 news 游标及受跟踪 Bug 消息的状态。只能恢复最后一次成功原子写入的内容。 |
| Owner 认领、已注册群组、enrollment nonce 和待完成的管理员提升 | `settings.json` 会恢复这些内容，并为已持久化的待完成提升重建离群截止时间。 |
| 设置面板会话和草稿 | 不保留。这些未提交状态最多 256 份，30 分钟后过期；请重新执行 `/settings`。 |
| 当日批准和拒绝计数、管理员正向缓存、私聊和查询限速窗口、查询缓存、清理计时器及通知限速状态 | 不保留。这些内容是有界运行缓存或当日观测数据，不属于持久状态。 |

一个注册宽限路径尚未持久化：机器人已有 owner，且在没有 enrollment payload 的情况下以普通
成员身份加入群组时，十分钟离群截止时间只存在于 `registrationService.waiting`。如果进程在
到期前崩溃或重启，该截止时间会丢失，机器人可能继续留在未授权群组。管理员应手动移除机器人
或重新执行 enrollment 流程。持久化和恢复该截止时间需要修改
`cmd/gentoo-zh-verify-bot/registration.go`。

已发送的 `kernel` 内核题有一项状态尚未及时持久化：`markPrompted` 修改内存中的 `prompted` 标志时，不会
立即保存 `pending.json`。如果进程在下一次状态变更或优雅停止前崩溃，申请会恢复为未发送问题的
状态，因此申请人的回复不会参与判定。消除该窗口需要修改 `internal/verify/service.go`。

手动运行时，如果未设置 `STATE_DIRECTORY`，所有文件状态都只存在于内存中。持久 owner 认领、
enrollment capability 和运行时群组注册会失败，不会报告未持久化的成功。各文件的详细语义见
[状态与持久化](state-persistence.md)。

### Telegram 中断与积压恢复

程序会先注册全部处理器，再启动 `UpdatesViaLongPolling`，因此启动时的积压 update 不会在对应
处理器存在之前被消费。初始 offset 为零。Telego 会把该值复制到私有 poll 参数中；收到 update
后才将 offset 更新为 `update_id + 1`，`GetUpdates` 失败时不会修改 offset。非零的
`WithLongPollingRetryTimeout(5s)` 会无限重试，不会关闭 update channel。进程重启后再次从零
开始，要求 Telegram 返回尚未确认的最早 update；应用代码不会重置正在使用的 offset，也不会在
poll 失败时跳过 update。

网络中断一小时后，poll 仍在重试，心跳逻辑会暂停验证截止时间。连接恢复后，Telegram 会重新
投递排队的 update；待验证申请会获得新的时间窗口，并在既有限制内重新通知。如果 update stream
在进程 context 仍有效时关闭，程序会以非零状态退出；`Restart=always` 随后启动新进程。

如果 token 在启动时被拒绝，必要的 `GetMe` 调用会失败；进程退出后，systemd 每 30 秒重试一次。
如果 token 在运行期间失效，poll 和心跳调用都会失败。调用仍能结束，说明循环继续取得进展，因此
不会触发 watchdog；poll 会持续重试，直到管理员更换 token 并重启服务。

Telegram 只为断线机器人保留约 24 小时的 update。更早的入群申请 update 可能永久丢失，Bot API
也无法枚举群组中仍待处理的申请。中断超过该期限后，第一次成功心跳会把现有
`heartbeat.json` 时间戳与保留期比较，并针对每个受保护群组发送一次对应语言的通知。通知发送到
`admin_log_chat_id`；未配置管理员日志时发送到受保护群组。管理员随后必须手动检查 Telegram 的
待处理入群申请队列。

### 内存上限

`MemoryMax=512M` 是安全边界，不是常驻内存目标。常驻内存主要来自 Go runtime、Telego 缓冲区、
软件包和 news 缓存、配置与 i18n 数据，以及有界状态 map。Update channel 最多保留 100 个
update；同时执行的 update 处理器不超过 64 个；待验证申请全局最多 2000 个，每个群组最多 500
个；警告、失败记录、面板、管理员缓存、查询限速、清理计时器和 feed 跟踪结构也有明确上限。

因此，入群洪泛不会使待验证 map 或处理器 goroutine 无限增长；超出上限的申请会留在 Telegram
中，等待管理员处理。并发外部查询及其有界响应正文会产生最大的短时内存分配。如果 cgroup 达到
512 MiB，内核会通过 OOM 终止服务；systemd 会记录 OOM 失败，并在 30 秒后重启。

### 在线验证

安装 unit 后，重新加载配置并检查生效值：

```sh
sudo systemctl daemon-reload
sudo systemctl restart gentoo-zh-verify-bot.service
systemctl show gentoo-zh-verify-bot.service \
  -p Type -p NotifyAccess -p Restart -p RestartUSec \
  -p StartLimitIntervalUSec -p StartLimitBurst \
  -p WatchdogUSec -p TimeoutStopUSec -p MemoryMax
```

预期值包括 `Type=notify`、`NotifyAccess=main`、`Restart=always`、
`RestartUSec=30s`、`StartLimitIntervalUSec=0`、`StartLimitBurst=5`、
`WatchdogUSec=2min`、`TimeoutStopUSec=30s` 和 `MemoryMax=536870912`。

检查由 `sd_notify` 驱动的状态和最近一次 watchdog 信号：

```sh
systemctl show gentoo-zh-verify-bot.service \
  -p ActiveState -p SubState -p Result -p MainPID -p NRestarts \
  -p WatchdogTimestamp -p WatchdogTimestampMonotonic
```

收到 `READY=1` 前，unit 保持 `ActiveState=activating`。随后应显示
`ActiveState=active` 和 `SubState=running`。间隔 30 至 50 秒再次执行命令时，
`WatchdogTimestampMonotonic` 应更新。

在维护时段内，可在不停止 unit 的情况下验证正常退出和崩溃恢复：

```sh
sudo systemctl kill --kill-whom=main --signal=SIGTERM gentoo-zh-verify-bot.service
sleep 35
systemctl show gentoo-zh-verify-bot.service -p ActiveState -p SubState -p MainPID -p NRestarts

sudo systemctl kill --kill-whom=main --signal=SIGKILL gentoo-zh-verify-bot.service
sleep 35
systemctl show gentoo-zh-verify-bot.service -p ActiveState -p SubState -p MainPID -p NRestarts -p Result
```

两种情况下，`MainPID` 都应变化，`NRestarts` 都应增加。重复执行 SIGKILL 超过五次后，unit 仍应
恢复为 `active/running`；`StartLimitIntervalUSec=0` 可避免 `start-limit-hit`。若要验证
watchdog，应让主进程停止取得进展，并在 120 秒后检查日志；systemd 应记录 watchdog 失败、增加
`NRestarts`，再把 unit 恢复为 `active/running`。

```sh
sudo systemctl kill --kill-whom=main --signal=SIGSTOP gentoo-zh-verify-bot.service
journalctl -fu gentoo-zh-verify-bot.service
```

每次 SIGTERM 验证后，应在日志中确认有界停止和状态写入：

```sh
journalctl -u gentoo-zh-verify-bot.service -b --grep='shutdown:'
systemctl show gentoo-zh-verify-bot.service \
  -p MemoryCurrent -p MemoryPeak -p MemoryMax -p OOMPolicy -p Result
```
