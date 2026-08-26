# 配置参考

`config.json` 是可选的。没有这个文件时，机器人以「没有预置群组」的状态启动，等待运行时注册，这也是添加群组的常规方式。下面每一项都有可用的默认值，因此首次部署通常只需要环境变量里的 `BOT_TOKEN`。

取值顺序在任何地方都一致：`settings.json` 中的运行时覆盖优先，其次是 `config.json`，最后是内置默认值。修改 `config.json` 需要重启服务；在设置面板中修改不需要。

## 环境变量

| 变量 | 用途 |
| --- | --- |
| `BOT_TOKEN` | 必填。Telegram 机器人令牌。 |
| `GITHUB_TOKEN` | 可选。提高 overlay 查询使用的 GitHub API 配额。 |
| `TELEGRAM_API_URL` | 可选。指向自建的 Bot API 服务器。 |

## 优先使用设置面板

大多数设置都可以通过 `/settings` 按群修改，无需重启。写进 `config.json` 只是设定初始值：

验证开关 · 验证题发送方式 · 验证方式 · 姓名遮盖 · 封禁时长 · 禁言时长 · 警告上限 · 验证超时 · 最多失败次数 · 重试冷却 · 被邀请成员是否验证 · 查询结果自动删除及保留时间 · 群组语言 · 必加频道与邀请链接 · 选择题库与备用题库 · 频道身份发言拦截与白名单 · 受信任群组 · 已知聊天 · 富文本输出 · 告警聊天 · 全局私聊查询频率。

## 两个版本

`gentoo-zh-verify-bot` 用 `-tags gentoo` 构建，Gentoo 查询使用短名 `/pkg` `/use` `/bug` `/news`
`/bbs` `/arm`。`gentoo-zhbot` 是面向一般 Linux 社区的默认构建，上述命令加 `g` 前缀，`/pkg`
留给群组自己使用。两者都提供 `/pkgs` `/distro` `/armpkgs` `/wiki` `/kernel` `/man` `/cve`
`/repology`。其余没有差别，也没有任何配置项在两者之间切换：选择的是安装哪个二进制。

## 字段

全新部署中值得手工填写的只有 `group_ids`，而且仅在你已经知道群组 ID 时才需要。

### 群组

| 字段 | 默认值 | 含义 |
| --- | --- | --- |
| `group_ids` | 无 | 受保护群组 ID。`groups` 接受同一份列表并支持按群设置；`group_id` 是旧的单数写法。 |
| `control_group_id` | 第一个有效群组 | 其管理员可以修改机器人全局设置的群组。该值必须是本文件中配置过或运行时注册过的群组，否则启动失败。 |
| `known_chat_ids` | 无 | 机器人留在其中但不验证的聊天，不等于免验证。 |
| `trusted_member_group_ids` | 无 | 已在其中的成员完全跳过验证。 |

### 验证

| 字段 | 默认值 | 含义 |
| --- | --- | --- |
| `verify_mode` | `kernel` | `kernel`、`quiz` 或 `mixed`。题库为空时回退到 `kernel`。 |
| `delivery_mode` | `both` | `group`、`dm` 或 `both`。 |
| `timeout_seconds` | `240` | 申请人可用的时长。进群后才验证的成员默认十分钟，除非在面板中设置了本项。 |
| `verify_max_fails` | `3` | 触发自动封禁的失败次数。负值关闭。 |
| `verify_retry_seconds` | `180` | 失败后的冷却时间，重新申请与重新加入都要等待。负值关闭。 |
| `verify_invited` | `true` | 被他人邀请入群的成员是否仍需验证。 |
| `ban_seconds` | `0`（永久） | 自动封禁的时长。 |
| `questions` | 无 | 选择题库。没有内置题库：题库为空时 `verify_mode: quiz` 回退到 `kernel`。参见 [`examples/quiz-bank.json`](../../examples/quiz-bank.json)。 |
| `fallback_questions` | 内置 | 面向没有 Linux 设备的申请人的简答题库。参见 [`examples/fallback-questions.json`](../../examples/fallback-questions.json)。 |

### 必加频道

| 字段 | 默认值 | 含义 |
| --- | --- | --- |
| `required_channel_id` | `0`（停用） | 申请人必须加入的频道。 |
| `channel_display` | 无 | 频道的 `@handle`。设置 `required_channel_id` 后必填，除非改用 `channel_invite_url`；两者都没有时启动失败。 |
| `channel_invite_url` | 无 | 仅当频道是没有公开用户名的私有频道时需要。 |
| `required_channel_fail_open` | `true` | 无法读取频道成员状态时是否仍然放行。 |

### 群管理

| 字段 | 默认值 | 含义 |
| --- | --- | --- |
| `warn_limit` | `3` | 触发自动移出群组的警告次数。 |
| `mute_seconds` | `3600` | `/mute` 的默认时长，始终有限。 |
| `block_channel_senders` | `false` | 删除并封禁以频道身份发出的消息。 |
| `channel_whitelist` | 无 | 不受上一项限制的发送方聊天。 |
| `admin_log_chat_id` | `0` | 运维告警的去向。为 0 时发到出问题的群。 |

### 消息与查询

| 字段 | 默认值 | 含义 |
| --- | --- | --- |
| `lang` | `zh` | `zh`、`zh-Hant` 或 `en`。 |
| `notify_ttl_seconds` | `60` | 群内临时提示的保留时间。 |
| `lookup_ttl_seconds` | `180` | 群内查询结果的保留时间。私聊消息永不定时删除。 |
| `private_query_per_min` | `3` | 每个用户的私聊查询频率。 |
| `rich_messages` | `false` | 使用富文本输出，并保留 HTML 回退。 |
| `private_reply` | 内置 | 对非命令私聊消息的回复。 |
| `overlays` | `gentoo-zh/overlay`、`gentoo/guru` | `/pkg` 搜索的 overlay。 |
| `news_url` | gentoo.org 新闻条目 | `/news` 的数据源（通用版为 `/gnews`）。 |
| `user_agent` | `gentoo-zh-verify-bot` | 出站 HTTP User-Agent。 |
| `stats_timezone` | `Asia/Shanghai` | `/stats` 的日界时区。 |

### 所有权与推送

| 字段 | 默认值 | 含义 |
| --- | --- | --- |
| `owner_claim_lifetime_seconds` | `600` | 首次启动时写入日志的一次性认领链接的有效期。 |
| `owner_claim_user_id` | 无 | 将该认领限制给指定的 Telegram 用户。日志可被他人读取时值得设置。 |
| `feeds` | 无 | Bugzilla 与新闻推送目标。参见 [`examples/feeds.json`](../../examples/feeds.json) 和[自动推送](feed.md)。 |
