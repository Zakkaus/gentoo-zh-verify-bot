# 管理操作

管理命令只在受保护群组中处理。除非另有说明，调用者必须是非匿名群组管理员，并用命令回复目标消息。

## 授权和目标检查

**实现位置：**`internal/moderate` 包；`internal/moderate/service.go` 中的 `(*Service).warnPrecheck` 和 `(*Service).isGroupAdmin`；`internal/tg` 包；`internal/tg/tg.go` 中的 `(*Client).FreshAdmin` 和 `(*Client).CachedAdmin`。

调用者权限每次都从 Telegram 实时查询。查询失败时按无权限处理，不执行管理操作。`/ban`、`/sb`、`/mute` 和 `/warn` 还通过肯定结果缓存检查目标，目标是管理员时拒绝操作。目标权限查询失败也会拒绝。`/unmute` 和 `/clearwarn` 不检查目标是否为管理员。

缺少回复、缺少目标用户、群组不受保护、命令发送者匿名或调用者授权失败时，不执行目标操作。处理器确认该更新属于受保护群组后，仍会尽力删除命令消息。

## 封禁和清理

**实现位置：**`internal/moderate` 包；`internal/moderate/service.go` 中的 `(*Service).OnBan`、`(*Service).OnPurge` 和 `(*Service).moderate`；`internal/tg` 包；`internal/tg/tg.go` 中的 `(*Client).Ban`。

`/ban` 按群组当前封禁时长调用 Telegram 成员封禁，不撤回历史消息。`/sb` 使用相同时长，并设置 `revoke_messages=true`，由 Telegram 尝试删除该用户在群内的历史消息。代码不会枚举或核实实际删除了哪些历史消息。两条命令都先封禁，再显式删除被回复的消息，因此封禁失败时会保留证据。封禁成功后，删除目标消息、清理命令、发送群内通知和管理日志告警均为尽力而为。

机器人缺少封禁成员权限，或 Telegram 拒绝封禁时，程序记录错误、发送失败通知，并在未配置管理日志群组时把失败告警发送到当前群组。此时不会显式删除目标消息。通知发送失败会被忽略；封禁不会自动重试。

## 禁言和解除禁言

**实现位置：**`internal/moderate` 包；`internal/moderate/service.go` 中的 `(*Service).OnMute` 和 `(*Service).OnUnmute`；`internal/tg` 包；`internal/tg/tg.go` 中的 `(*Client).Mute` 和 `(*Client).Unmute`。

`/mute` 使用已配置的有限默认时长，也可在命令中指定。少于 30 秒会调整为 30 秒，超过 Telegram 上限会调整为 366 天。禁言不接受永久或零时长。只有 Telegram 接受限制后，程序才删除目标消息。时长解析失败时只显示用法，不改变目标状态。

`/unmute` 在 Telegram 返回群组默认权限时恢复该权限；无法取得默认权限时，发送显式的完整权限集合。因为该命令不检查目标管理员状态，所以可以对管理员执行。Telegram 调用失败时发送失败通知，不发送成功通知。两条命令都不会自动重试。

缺少限制成员权限会导致 Bot API 调用失败。`/mute` 会保留目标消息，并向 `admin_log_chat_id` 发送包含群组和目标信息的运维告警；未配置管理日志时，告警发送到当前群组。`/unmute` 不会改变权限。命令删除和通知发送分别依赖删除消息及发送消息能力。

## 警告和自动移出

**实现位置：**`internal/moderate` 包；`internal/moderate/service.go` 中的 `(*Service).OnWarn` 和 `(*Service).warnKick`；`internal/moderate/state.go` 中的 `(*warningState).increment` 和 `(*warningState).save`。

`/warn` 增加当前群组与用户对应的计数，并立即尝试保存。未达到 `warn_limit` 时，只报告新计数。达到阈值时，程序先执行不撤回历史消息的永久封禁，再使用 `only_if_banned=true` 立即解除封禁，使用户退出群组后可以重新申请。

移出过程中的封禁失败时，通常是机器人缺少限制成员权限。已经增加的计数会保留在阈值或更高位置，用户仍在群内，程序发送失败通知和运维告警。下一次警告会再次尝试移出。封禁成功但解除封禁失败时，程序清除计数，并说明用户可能仍处于封禁状态。两次调用均成功时也会清除计数。

警告状态写入错误由共享存储记录日志，命令本身不处理该错误。内存计数和管理操作仍会继续，重启后可能丢失最新计数。

## 清除警告

**实现位置：**`internal/moderate` 包；`internal/moderate/service.go` 中的 `(*Service).OnClearWarn`；`internal/moderate/state.go` 中的 `(*warningState).clear` 和 `(*warningState).save`。

`/clearwarn` 删除被回复用户在当前群组中的计数，并报告原值，包括零。调用者必须是管理员，但目标可以是管理员。该命令不会改变 Telegram 成员限制。

保存失败不会反馈给管理员。计数在内存中保持清除；旧文件未替换时，重启可能恢复旧计数。缺少删除消息权限时，命令消息可能保留，但计数仍会清除。

## 频道身份过滤

**实现位置：**`internal/moderate` 包；`internal/moderate/antispam.go` 中的 `(*Service).FilterChannelSenders` 和 `(*Service).OnBC`；`internal/bot` 包；`internal/bot/bot.go` 中的 `(*Service).Register`。

过滤器作为中间件先于命令处理器执行。当前群组启用反垃圾设置后，过滤器针对 `sender_chat` 为其他频道身份的消息。以下消息会继续处理：

- 普通用户消息；
- 匿名管理员以群组自身身份发送的消息；
- Telegram 从关联讨论频道自动转发的消息；
- 已配置或注册的群组、必加频道、受信任群组、feed 目标、管理日志群组和已知群组；
- 当前群组白名单中的频道身份。

遇到不受信任的频道身份时，程序先尝试删除消息，再尝试封禁该频道身份，并向管理日志群组报告封禁是否成功。该更新随后被消费，不再进入其他处理器。删除失败不阻止封禁。封禁失败会记录并报告，但不会恢复已经删除的消息。未配置管理日志群组时，此类告警不会回退到当前群组。

无参数 `/bc` 切换当前群组的过滤器。`/bc allow <id>` 先提交白名单，再解除频道身份封禁；解除失败时白名单保持生效，并显示部分失败。`/bc deny <id>` 删除白名单项，但不会立即封禁；该频道身份再次发言时才触发过滤。ID 可使用 Bot API 的 `-100...` 形式，或使用 `t.me/c/...` 中的纯数字部分；完整 URL 不受支持。白名单最多保留 4,096 项，新增内容超过上限时淘汰最早的项目。

Telegram 只有在 BotFather 隐私模式关闭后才会把全部频道身份消息交给机器人。该条件由 Telegram 控制，不属于仓库实现。

## 权限缺失和部分失败

**实现位置：**`internal/moderate` 包；`internal/moderate/service.go` 中的 `(*Service).CheckGroupSetup` 和 `(*Service).LogGroupSetup`；`internal/tg` 包；`internal/tg/tg.go` 中的 `(*Client).Delete`、`(*Client).Notify` 和 `(*Client).FailAlert`。

启动或注册后的自检会报告群组访问、管理员状态、邀请用户、封禁用户、删除消息，以及必加频道管理员状态。该检查只提供诊断，不阻止运行时处理器调用 Telegram。

| 缺少的能力 | 运行时结果 |
| --- | --- |
| 调用者成员查询 | 授权按失败处理，不执行目标操作。 |
| 邀请用户 | 验证批准失败，待验证记录重新开放；管理命令不使用该权限。 |
| 封禁或限制成员 | `/ban`、`/sb`、`/mute`、`/unmute`、警告移出、验证封禁和频道身份封禁在 Telegram 调用处失败。此前完成的警告计数或白名单提交不会回滚。 |
| 删除消息 | 所有清理均为尽力而为。主要管理操作可能成功，但命令或目标消息仍保留。 |
| 发送消息 | 通知和告警可能缺失。主要管理调用仍可能已经成功；通知发送不属于同一事务。 |
| 必加频道管理员 | 验证可能无法读取频道成员；处理方式见[申请者流程](applicant.md)。 |

Telegram 调用、设置写入和警告写入之间没有通用回滚。修改代码时，应保留前述顺序，或明确改变该行为及相应测试。
