# gentoo-zh-verify-bot

[English](README.md) | 简体中文

`gentoo-zh-verify-bot` 是面向 Gentoo 中文社区群组的 Telegram 入群验证机器人，用于筛除批量提交入群申请的垃圾账号。

四选一按钮题允许仅随机点击的账号以 1/4 概率通过。因此，默认验证要求申请人**手动输入正在运行的 Linux 内核版本**。私聊提示还包含一条按申请人变化、默认折叠显示的指令，用于识别替垃圾账号作答并遵循该指令的 LLM 智能体。

机器人是一个静态链接的 Go 二进制文件，采用长轮询，不开放入站端口。项目只有一个直接依赖：[telego](https://github.com/mymmrac/telego)，即 Telegram Bot API 绑定。

## 验证流程

1. Telegram 发来入群申请后，机器人保持申请待审，并默认立即尝试通过私聊发送该群组的验证题。只有申请人以前启动过此机器人时，Telegram 才允许机器人主动私聊。私聊送达后，机器人不会再发送群内提示。
2. Telegram 表示机器人不能主动发起会话时，机器人改为在群内发送带有按群 `verify_<groupID>` 深层链接的提示。申请人点击后，会在私聊中打开该群组的对应申请。管理员可以在 `/settings` 中按群停用优先私聊验证。
3. 可信群组成员可免验证；管理员也可配置批准前必须加入的频道。申请人完成指定验证后，机器人批准申请。答错或超时会被拒绝；管理员也可在群内回退消息中选择 `👮 直接通过` 或 `🚫 拒绝并封禁`。

| 模式 | 申请人操作 | 说明 |
| --- | --- | --- |
| `kernel`（默认） | 执行 `uname -r` 并手动发送结果 | 最多尝试 3 次。没有 Linux 设备时，可提供当前分钟数，再回答 `fallback_questions` 中不显示答案的简答题。 |
| `quiz` | 点击 `questions` 中随机打乱后的正确选项 | 适用于不要求申请人使用 Linux 的群组。 |
| `mixed` | 完成随机选中的一种验证 | 每项待验证申请独立选择。 |

申请人看到的验证文案按 Telegram `language_code` 使用简体中文、繁体中文或英文。群组和管理员输出使用该群组的有效 `lang`：全局值是基线，`groups[].lang` 可以覆盖全局值，设置面板还能保存按群运行时覆盖值。配置中的 `questions` 按原文显示，不会自动翻译。

验证失败后进入 `verify_retry_seconds` 冷却期。在失败计数有效期内达到 `verify_max_fails` 次会自动封禁；验证成功后清除该申请人的失败计数。待验证状态最多保留 2,000 项，每个群组最多 500 项。达到任一上限后，新申请留给管理员手动审核，管理告警每 10 分钟最多发送一次。

## 本仓库自行实现的部分

telego 只提供 Bot API 传输和类型。以下验证、恢复、持久化、管理及 Gentoo 语义均由本仓库基于标准库实现；外部数据仍来自所列的上游服务。链接文件是审查或修改对应行为的入口。

- **内核版本验证**：[`internal/verify/kernel.go`](internal/verify/kernel.go) 可解析单独的版本号、常见英文说明、中文说明、WSL 输出，以及粘贴的 `uname -a` 或 `/proc/version` 形式输出。ASCII 上下文采用有限白名单，因此 `model=GPT-5.2` 等无关版本标识无法通过。1.x 仅接受 1.0–1.3，2.x 仅接受 2.0–2.6；3.x–30.x 均在预留范围内，不受当前内核主版本限制。
- **AI 代答陷阱**：[`internal/verify/kernel.go`](internal/verify/kernel.go) 生成并检查与 nonce 绑定的 `AGENT-… model=…` 回复，[`internal/verify/state.go`](internal/verify/state.go) 则将统计写入 `agents.json`，并为 `/stats` 生成输出。因此，不存在可供所有申请共用的固定 token。完全匹配时，机器人会拒绝申请。对方声明的模型仅用于统计，不构成证据；该机制用于威慑，不是安全边界。
- **中断恢复**：[`internal/verify/state.go`](internal/verify/state.go) 负责 Telegram heartbeat、到期探测、重新设置完整时限和有上限的通知。Telegram 无法访问时，机器人不会拒绝申请或增加失败计数，而是重新提供完整验证时限。持续中断恢复后，所有进行中的申请都会获得新截止时间；机器人还会重新发送私聊通知和群验证消息，每次恢复最多通知 30 人，并抑制网络反复波动产生的重复通知。
- **状态写入**：[`internal/store/json.go`](internal/store/json.go) 将快照创建和提交串行化，先写入同目录临时文件，执行 `fsync`，再原子重命名并同步目录；[`internal/verify/state.go`](internal/verify/state.go) 等子系统加载器在无法安全读取状态后停止写入。JSON 格式损坏时，原文件先移至 `<name>.corrupt`，然后才创建新状态。`pending.json`、`warns.json`、`antispam.json`、`verifyfail.json`、`settings.json` 和 `heartbeat.json` 无法读取时，对应路径会停止写入，避免覆盖仍可恢复的数据。
- **Gentoo 语义**：[`internal/lookup/packages.go`](internal/lookup/packages.go) 负责 `/pkg`、`/use` 和 `/arm`。该文件按数值比较 revision 和 Gentoo 后缀，例如 `-r10` 高于 `-r2`，并遵循 `_alpha < _beta < _pre < _rc < release < _p`。USE 输出区分本地、全局 USE flag 和 USE_EXPAND 组，并从 overlay 读取 `IUSE` 与 `metadata.xml`。arm64 结果区分稳定、测试、无 keyword 和数据源不可用。[`internal/lookup/distros.go`](internal/lookup/distros.go) 负责 `/pkgs`、`/distro` 和 `/armpkgs`，分开处理 RHEL 兼容发行版、CentOS Stream 与 EPEL，并根据实时发行版元数据解析 Debian 和 Ubuntu 的发行版状态，不写死版本号。

## 群组管理
管理员须以个人身份发送命令，并回复目标消息。

| 命令 | 作用 |
| --- | --- |
| `/mute [时长]` · `/unmute` | 限时禁言，默认 1 小时，例如 `/mute 30m`；`/unmute` 可提前解除。 |
| `/ban` | 移出并封禁，时长由 `/bantime` 决定；只删除被回复的消息。 |
| `/sb` | 封禁后清除该用户的全部消息。 |
| `/warn` · `/clearwarn` | 增加或清除警告；达到 `warn_limit` 后自动移出群组。 |
| `/bantime` | 设置 `0` 表示永久，也可使用 `7d`、`12h` 或 `30m`；1–29 秒按 30 秒处理，超过 366 天按永久封禁处理。 |
| `/bc` | 拦截以频道身份发送的消息并管理白名单。需要关闭 BotFather 隐私模式；状态会持久化。 |

`/start` 和 `/stop` 修改当前群组是否启用验证；`/vmode` 修改该群组的验证模式；`/spoiler` 修改该群组是否隐藏申请人名称；`/autodel` 修改该群组的查询结果清理策略；`/bantime` 修改该群组的封禁时长；`/bc` 修改该群组的频道身份发言过滤器和白名单。这些命令都提交稀疏的按群覆盖值。只有 `/rich` 修改机器人级富文本输出，也是唯一受 `control_group_id` 限制的命令。`/ping`、`/stats` 和 `/help` 只显示运行状态；`/stats` 除每日批准和拒绝人数外，还显示 AI 代答陷阱的累计统计。

## Gentoo 与 Linux 查询
查询命令也可在私聊中使用，每分钟限制为 `private_query_per_min` 次。

| 命令 | 结果 |
| --- | --- |
| `/pkg <包名>` | Gentoo 官方树和已配置 overlay 中的软件包与版本。 |
| `/use <包名>` | USE、USE_EXPAND、软件包信息和版本。 |
| `/bug <编号>` | Gentoo Bugzilla 工单。 |
| `/news [关键词]` | Gentoo 新闻。 |
| `/wiki <关键词>` | Gentoo 和 Arch wiki，优先返回简体中文页面。 |
| `/bbs <关键词>` | Arch Linux CN 结果和英文论坛搜索链接。 |
| `/pkgs <包名>` · `/distro <包名>` | [Repology](https://repology.org) 中按发行版及稳定、测试等状态标注的版本。 |
| `/arm <包名>` | Gentoo `arm64` keyword 状态。 |
| `/armpkgs <包名>` | Gentoo、Debian、Ubuntu、Fedora、Arch ARM 和 AUR 的 arm64 支持。 |

可选播报功能按 `feed` 或 `feeds` 中的目标轮询 Gentoo Bugzilla 和新闻，游标可跨重启保留。bug 状态变化时，机器人编辑原消息；确认状态可补发一次 `🔔` 通知，关闭后则将 `🐞` 改为 `✅` 或 `❌`。

## 部署

`BOT_TOKEN` 是唯一必填项。随附的 systemd unit 会创建私有状态目录；`config.json` 可省略。

### 从源代码构建

从源代码构建需要 Go 1.26.7 或更高版本。构建二进制文件，并将 unit 复制到当前目录：

```sh
CGO_ENABLED=0 go build -trimpath -o gentoo-zh-verify-bot ./cmd/gentoo-zh-verify-bot
cp deploy/gentoo-zh-verify-bot.service .
```

### 安装预构建发布文件

发布文件的名称固定为 `gentoo-zh-verify-bot-linux-amd64`、`gentoo-zh-verify-bot-linux-arm64` 和 `SHA256SUMS`。将 `version` 设为发布 tag，将 `arch` 设为本机架构，然后下载并校验二进制文件。unit 不在发布文件中，须从同一个 tag 获取：

```sh
version=v3.12.0
arch=amd64 # 64 位 Arm 使用 arm64
release_url="https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/download/${version}"
curl --fail --location --remote-name "${release_url}/gentoo-zh-verify-bot-linux-${arch}"
curl --fail --location --remote-name "${release_url}/SHA256SUMS"
sha256sum --ignore-missing --strict --check SHA256SUMS
mv "gentoo-zh-verify-bot-linux-${arch}" gentoo-zh-verify-bot
curl --fail --location \
  "https://raw.githubusercontent.com/Zakkaus/gentoo-zh-verify-bot/${version}/deploy/gentoo-zh-verify-bot.service" \
  --output gentoo-zh-verify-bot.service
```

### 安装并启动服务

安装二进制文件和 unit。先创建权限模式为 `0600` 的空 token 文件，再通过编辑器写入 `BOT_TOKEN=<your-token>`；token 不会出现在 shell 参数或 shell 历史记录中。

```sh
sudo install -Dm755 gentoo-zh-verify-bot /usr/local/bin/gentoo-zh-verify-bot
sudo install -Dm600 /dev/null /etc/gentoo-zh-verify-bot/bot.env
sudoedit /etc/gentoo-zh-verify-bot/bot.env
sudo install -Dm644 gentoo-zh-verify-bot.service /etc/systemd/system/gentoo-zh-verify-bot.service
sudo systemctl daemon-reload
sudo systemctl enable --now gentoo-zh-verify-bot
```

从 journal 读取仅可使用一次的私有所有者认领链接：

```sh
sudo journalctl -u gentoo-zh-verify-bot
```

在 Telegram 中打开该链接。未使用的链接默认十分钟后过期。认领完成前，任何可读取 journal 的用户都可使用该链接成为所有者。设置 `owner_claim_user_id` 可将认领权限限制为一个账号。

将机器人添加到群组并提升为管理员，同时开启**邀请用户**、**封禁用户**和**删除消息**权限。机器人会将所有者授权的群组写入 `settings.json`，不会写入 `config.json`。

需要委托登记时，所有者在私聊中向机器人发送 `/enroll`，再将生成的一次性群组链接交给群组管理员。链接有效期为十分钟。重复使用链接、使用过期链接，或未经所有者授权便提升机器人，都会使机器人退出未知群组。

需要移除运行时登记的群组时，所有者在私聊中执行 `/unregister <group-id>`。机器人会取消
登记、删除该群组的运行设置，然后尝试退出群组。仅将机器人移出群组不会删除登记状态；
如需主动移除，请执行 `/unregister`。

使用必需频道时，须将机器人设为频道管理员。普通频道成员无法读取其他用户的成员身份，因此不能执行成员身份门槛。启动时会为每个已登记群组生成一份合并报告，列出缺少的权限和修正方法。

## 配置

`BOT_TOKEN` 必填且没有默认值。可选的 `GITHUB_TOKEN` 无需 scope，可将 overlay 请求的 GitHub API 限额从每小时 60 次提高到约 5,000 次。可选的 `TELEGRAM_API_URL` 使用自建 Bot API server，未设置时使用 Telegram 官方 Bot API。

`config.json` 可省略。文件存在时，其内容作为经过验证的基线；JSON 格式错误或配置值无效仍会阻止启动。Telegram 中保存的稀疏运行时值优先于文件，文件值优先于内置默认值。修改文件后需要重启服务。

### 群组与验证
| 键 | 作用 | 默认值和归一化规则 |
| --- | --- | --- |
| `groups` | 可选的启动时群组种子及按群覆盖项：`id`、`required_channel_id`、`channel_display`、`channel_invite_url`、`trusted_member_group_ids`、`questions`、`verify_mode`、`lang`。 | 默认 `[]`；也可在运行时登记群组。配置文件中的 ID 必须非零且不得重复。空字段继承全局值；显式设置频道 ID `0` 或可信群组列表 `[]` 可对该群关闭相应门槛。 |
| `group_ids` / `group_id` | 旧版群组列表和单个群组字段，加载时合并到 `groups`。 | `[]` / `0`；旧字段中的重复 ID 合并到已有群组。 |
| `control_group_id` | 可执行 `/rich` 的受管理群组；`/rich` 是命令入口中唯一的机器人级设置。 | `0` 表示每个受管理群组的管理员都可执行。启动配置中的非零 ID 不属于已配置 `groups` 时，配置无效。 |
| `lang` | 群组和管理员输出的语言基线；`groups[].lang` 和设置面板可按群覆盖。 | 空值使用 `zh`。只接受 `zh`、`zh-Hant` 和 `en`；其它值会阻止启动。 |
| `required_channel_id` | 全局必需频道门槛。 | `0` 表示关闭。 |
| `channel_display` | 全局频道名称或公开 `@handle`。 | 空。 |
| `channel_invite_url` | 全局加入链接；私有频道没有 `@handle` 时必填。 | 空。 |
| `trusted_member_group_ids` | 可信群组成员免验证来源；无法读取成员状态时仍执行常规验证。 | `[]` 表示不启用。 |
| `known_chat_ids` | 允许机器人停留的其它聊天，不会因此成为受管理群组、必需频道或可信来源。 | `[]`。 |
| `owner_claim_lifetime_seconds` | 启动时写入 journal 的私有一次性所有者认领链接有效期。 | `0` 变为 600；负数无效；正数不变。 |
| `owner_claim_user_id` | 可使用首次所有者认领链接的 Telegram 用户 ID。 | `0` 不限制账号，任何取得链接的 journal 读取者均可认领；负数无效。 |
| `verify_mode` | 全局验证模式：`kernel`、`quiz` 或 `mixed`；按群配置和 `/vmode ...|auto` 可覆盖。 | 空值变为 `kernel`；其它值导致加载失败。 |
| `timeout_seconds` | 验证时限。 | `<=0` 变为 240；1–29 变为 30；上限 1,800。 |
| `required_channel_fail_open` | 申请人通过验证后，机器人无法读取必需频道成员状态时的处理方式。两种模式都会通知管理员。 | `true`（默认）批准；`false` 拒绝并允许重试。 |
| `verify_retry_seconds` | 验证失败后的冷却时间。 | `0` 变为 180；负数关闭；正数不变。 |
| `verify_max_fails` | 自动封禁前的失败次数。 | `0` 变为 3；负数关闭；正数不变。 |
| `fallback_questions` | 无 Linux 设备时的简答题库：`[{q,answers:[…]}]`。 | `[]` 使用内置多语言题库。每项需要非空 `q` 和至少一个非空完整答案。 |
| `questions` | 全局选择题库：`[{q,options:[…],answer}]`。 | 只有全部群组均为 `kernel` 模式时才可为 `[]`。`options` 至少两项；`answer` 默认为索引 0 且不得越界；`q` 按原文显示。 |

### 管理、消息与运行默认值
| 键 | 作用 | 默认值和归一化规则 |
| --- | --- | --- |
| `notify_ttl_seconds` | 多少秒后删除机器人发送的群消息。 | `0` 变为 60；负数保留消息；正数不变。 |
| `lookup_ttl_seconds` | 同时删除查询命令和回复；`/autodel` 将运行时覆盖值保存到 `settings.json`。 | 未设置变为 180；`0` 或负数关闭；正数不变。 |
| `warn_limit` | `/warn` 达到多少次后自动移出群组。 | `<=0` 变为 3；无上限。 |
| `private_query_per_min` | 每名用户每分钟可在私聊中执行的查询次数；受管理群组内不限次。 | `<=0` 变为 3；无上限。 |
| `ban_seconds` | `/ban`、`/sb` 和验证自动封禁的默认时长；`/bantime` 将运行时覆盖值保存到 `settings.json`。 | `<=0` 表示永久；1–29 变为 30；超过 366 天变为永久。 |
| `mute_seconds` | `/mute` 默认时长；禁言始终限时。 | `<=0` 变为 3,600；1–29 变为 30；超过 366 天变为 366 天。 |
| `admin_log_chat_id` | 接收管理操作和失败告警的专用聊天。 | `0` 表示关闭；不做归一化。 |
| `stats_timezone` | `/stats` 每日重置所用 IANA 时区。 | 空值或无效值变为固定 UTC+8。 |
| `rich_messages` | `/pkg` 和 `/use` 的富文本基线；`/rich` 将运行时覆盖值保存到 `settings.json`。 | `false`。 |
| `private_reply` | 验证流程外普通私聊的回复。 | 空值使用内置帮助文本。 |
| `block_channel_senders` | `/bc` 的初始过滤状态；需要关闭隐私模式。 | `false`；持久化的 `antispam.json` 优先。 |
| `channel_whitelist` | 允许发言的频道 ID 初始列表。 | `[]`；持久化的 `antispam.json` 优先。 |

### 查询与播报源
| 键 | 作用 | 默认值和归一化规则 |
| --- | --- | --- |
| `overlays` | `/pkg` 使用的 GitHub overlay：`[{name,repo,branch}]`。 | `[]` 使用 gentoo-zh 和 guru。空 `name` 变为 `repo`；空 `branch` 变为 `master`；`repo` 必须为 `owner/name`；实际名称不得重复。 |
| `news_url` | Gentoo 新闻索引。 | 空值变为 `https://www.gentoo.org/support/news-items/`。 |
| `user_agent` | 出站 HTTP User-Agent。 | 空值变为 `gentoo-zh-verify-bot`。 |
| `feed` / `feeds` | 单个播报目标或目标数组。 | 未设置或 `[]` 表示关闭；同一非零 `chat_id` 重复出现时只保留第一项。 |

| 播报键 | 作用 | 默认值和归一化规则 |
| --- | --- | --- |
| `chat_id` | 目标频道或群组；机器人须有发帖权限。 | `0` 表示关闭。 |
| `lang` | bug 字段标签和新闻播报的语言。 | `zh`、`zh-Hant` 和 `en` 有效，空值按 `zh` 处理；无法识别的值会阻止启动。 |
| `interval_seconds` | 轮询间隔。 | `<=0` 变为 300；1–59 变为 60；无上限。 |
| `bugs` / `news` | 分别启用新 Bugzilla 工单和新闻播报。 | 未设置变为 `true`。 |
| `bug_product` / `bug_component` | 可选 Bugzilla 过滤条件。 | 空值匹配全部。 |
| `silent_bugs` | 是否静默发送 bug。 | `true` 表示全部静默；未设置或 `false` 时只静默 UNCONFIRMED bug，并允许补发一次确认通知。 |

未知 JSON 键会被忽略，并记录 `WARNING: config: unknown key ...`。出现该告警时应修正配置文件中的拼写。

Telegram 设置界面不能修改播报目标、overlay 列表、新闻源、`stats_timezone` 或 `user_agent`。这些设置仍须写入 `config.json`，并在修改后重启服务。

## 运维

### Telegram 前置条件
1. 通过 [@BotFather](https://t.me/BotFather) 创建机器人。
2. 登记群组前，通过私有启动链接认领所有者身份。
3. 在每个已登记群组中，将机器人提升为管理员并开启**邀请用户**、**封禁用户**和**删除消息**权限，同时为群组启用入群申请。
4. 使用必需频道时，将机器人设为频道管理员；普通成员身份不足以读取其他成员。
5. 除非 `/bc` 需要检查以频道身份发送的消息，否则 BotFather 隐私模式可保持开启。

### 安装说明
从源代码构建需要 **Go 1.26.7+**，与 `go.mod` 一致。[Releases](https://github.com/Zakkaus/gentoo-zh-verify-bot/releases) 只包含两个静态链接的 Linux 二进制文件和 `SHA256SUMS`；须按[安装预构建发布文件](#安装预构建发布文件)中的步骤单独获取对应 tag 的 unit。模块路径未使用 `/vN` 后缀，因此不支持 `go install …@v3.x`。

随附的 unit 读取 `/etc/gentoo-zh-verify-bot/bot.env`，按需读取 `config.json`，通过 `DynamicUser=` 运行，并由 `StateDirectory=` 创建权限模式为 `0700` 的 `/var/lib/gentoo-zh-verify-bot`。长轮询只需要出站 HTTPS，不需要监听端口或反向代理。


### 状态与重启
未设置 `$STATE_DIRECTORY` 时，所有状态只保存在内存中，机器人会记录告警。随附的 unit 已设置该变量。状态目录只能由服务用户访问。

| `$STATE_DIRECTORY` 中的文件 | 跨重启保留的内容 |
| --- | --- |
| `pending.json` | 进行中的验证、尝试次数、nonce、题目和截止时间；重启后重新设置计时器。 |
| `warns.json` | 每名用户的 `/warn` 计数。 |
| `antispam.json` | `/bc` 状态和频道白名单。 |
| `verifyfail.json` | 验证失败计数和冷却状态。 |
| `settings.json` | 所有者身份、一次性登记凭据、运行时群组、控制群组选择和稀疏运行时设置覆盖值。 |
| `heartbeat.json` | 最近一次成功连接 Telegram 的时间，用于重启恢复。 |
| `agents.json` | AI 代答陷阱累计次数和对方声明的模型计数。 |
| `feed-<chat_id>.json` | 播报游标和已跟踪 bug 的消息 ID。 |

每日 `/stats`、限流窗口，以及查询、新闻和软件包缓存会在重启后重置。`settings.json` 中的运行时设置不会重置。

### 故障处理
| 故障 | 机器人行为 | 管理员操作 |
| --- | --- | --- |
| 未设置 `BOT_TOKEN`、现有配置中包含无效值，或启动阶段的必要 Telegram 请求失败 | 进程以非零状态退出；systemd 按 `Restart=on-failure` 重试。配置文件缺失是有效状态，服务会以零群组启动。 | 查看 journal 中第一条 fatal 日志，修正 token、文件或网络后重启。 |
| 运行期间 Telegram 无法访问 | 长轮询继续重试。验证超时会延后，不拒绝申请，也不增加失败计数；恢复后重新提供完整时限，并执行有上限的通知。 | 无需清理申请。heartbeat 告警持续出现时检查网络。 |
| 出现 `ERROR state load <path>: ...; writes disabled until restart` | 使用该路径的核心状态转为内存运行，不再持久化；原文件保留以便恢复。 | 立即停止服务，检查或恢复文件，修正所有权和权限后重启。等待不会恢复写入。 |
| 软件包或发行版数据源没有响应 | `/pkg`、`/use`、`/arm`、`/pkgs` 和 `/armpkgs` 会区分“未找到”和“数据源不可用”，并标记不完整结果。播报抓取失败时保留游标，留待下次轮询。 | 检查结果中标明的数据源，不要将不可用或不完整结果解释为不存在。 |
| 无法读取必需频道成员状态 | 机器人通知管理员。申请人通过验证后，默认的 `required_channel_fail_open: true` 会批准；设置为 `false` 时拒绝并允许重试。 | 恢复机器人的频道管理员身份；默认策略不合适时应显式选择开放或关闭回退。 |
| 某个群组的启动检查报告显示设置未就绪 | 缺少一项或多项必要群组权限，或无法读取必需频道成员身份。 | 完成该群组唯一一份报告中的全部操作，再重启服务以重新执行检查。 |
| 播报目标无法访问或没有发帖权限 | 启动时记录告警；暂时性发送失败不会越过未发送项目。 | 修正 `chat_id`，加入机器人，并授予频道发帖权限。 |

## 为其它社区创建 fork

配置可以修改 Telegram 群组登记、验证题库和备用题库、overlay、Gentoo 新闻索引、播报目标，以及消息和运行策略。全局和按群语言可以在三个现有 locale 中选择。应优先使用 `groups` 或运行时登记、`questions`、`fallback_questions`、`lang`、`overlays` 和 `news_url`；这些修改不需要 fork 专用代码。

完整的社区 fork 仍须逐项修改代码和文档：

- 修改 `go.mod` 中的 module path 和所有 Go import。重命名 `cmd/gentoo-zh-verify-bot`、`deploy/` 和 `.github/workflows/release.yml` 中的命令目录、二进制文件和发布文件、默认 `/etc` 配置路径、`/var/lib` 状态目录、环境文件及 systemd unit。
- 更新 `internal/i18n/catalog.go`、`internal/config/config.go`、`internal/bot/commands.go`、`internal/panel/codec.go` 和 `internal/panel/settings_panel.go` 中的 locale 注册和选择分支。如果 `zh` 不再是默认 locale，还须单独修改简体中文默认值。
- 如果不会始终配置 `fallback_questions`，请替换 `internal/i18n/locales/*/verification.json` 中的内置备用题库。
- 如果不会始终配置 `overlays`，请替换 `internal/lookup/packages.go` 中默认的 gentoo-zh 和 GURU overlay。
- 检查 `internal/lookup/content.go` 和 `internal/feed/feed.go` 中的 Gentoo Bugzilla 与播报端点；检查 `internal/lookup/content.go`、`internal/lookup/packages.go` 和 `internal/lookup/distros.go` 中的 Gentoo 新闻、wiki、软件包端点和发行版元数据；还要修改 `internal/bot/bot.go`、`internal/bot/commands.go`、所有 locale catalogue 及公开命令表中的查询命令集。
- 替换文档和发布 workflow 中的上游仓库、raw 文件、release、安全报告及 changelog URL。

## 许可证
MIT — 见 [LICENSE](LICENSE)。
