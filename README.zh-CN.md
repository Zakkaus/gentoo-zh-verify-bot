# gentoo-zh-verify-bot

[English](README.md) | 简体中文

`gentoo-zh-verify-bot` 是面向 Gentoo 中文社区群组的 Telegram 入群验证机器人，用于筛除批量提交入群申请的垃圾账号。机器人会保持申请待审，完成验证后再批准或拒绝；同时提供群组管理、Gentoo 与 Linux 查询，以及可选的 Bugzilla 和新闻 Feed。

## 适用条件与运行开销

机器人适用于启用入群申请的 Telegram 群组或超级群组。机器人必须是群组管理员，并具有**邀请用户**、**封禁用户**和**删除消息**权限；如果启用必需频道条件，还必须是该频道的管理员。只有 `/bc` 需要关闭 BotFather 隐私模式。

发布文件是面向 Linux `amd64` 和 `arm64` 的静态链接二进制文件。使用 Telegram 托管 Bot API 和默认数据源时，机器人只需要出站 HTTPS，不需要数据库、反向代理或入站端口。随附的 systemd unit 将持久状态放在私有目录中，并通过 `MemoryMax=512M` 设置内存上限；`512M` 是安全边界，不是常驻内存用量。

## 验证流程

每项入群申请都按群组独立处理。机器人默认先尝试通过私聊发送验证题；Telegram 明确拒绝私聊投递时，机器人改为在群内发送提示，其中的 `verify_<groupID>` deep link 只打开该群组的待验证申请。因为传输错误可能发生在 Telegram 已接收消息之后，所以投递结果不确定时不会再发送群内副本。管理员可以在 `/settings` 中按群关闭优先私聊。

| 模式 | 申请人操作 | 行为 |
| --- | --- | --- |
| `kernel`（默认） | 输入正在运行的 Linux 内核版本；可执行 `uname -r` 获取 | 最多输入 3 次。没有 Linux 设备时，申请人须先声明该情况并提供当前分钟数，再回答不显示答案的简答题。 |
| `quiz` | 点击题库中随机打乱后的正确选项 | 使用该群组的 `questions`；题库为空时回退到 `kernel`。 |
| `mixed` | 完成随机选中的 `kernel` 或 `quiz` | 每项申请独立选择；题库为空时使用 `kernel`。 |

`kernel` 和简答题提示包含按申请变化的 LLM 代答陷阱。完全遵循隐藏指令会导致验证失败；对方声明的模型只用于累计统计，因此该机制用于威慑，不是安全边界。

群组可以让可信群组的成员免验证，也可以要求申请人在批准前加入指定频道。答错或超时会拒绝申请并进入冷却期；默认在 6 小时计数窗口内失败 3 次会触发自动封禁。申请人文案根据 Telegram `language_code` 使用简体中文、繁体中文或英文；群组和管理员文案使用该群组的 `lang`。

## 安装

`BOT_TOKEN` 是唯一必填的启动配置。从预构建发布文件安装不需要 Go；从源代码构建需要 Go 1.26.7 或更高版本。

### 使用发布文件

[Releases](https://github.com/Zakkaus/gentoo-zh-verify-bot/releases) 提供 `amd64`、`arm64` 二进制文件和 `SHA256SUMS`，不包含 systemd unit。将 `arch` 改为目标架构，并从同一 tag 获取二进制文件和 unit：

```sh
version=v3.12.0
arch=amd64
release_url="https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/download/${version}"
curl --fail --location --remote-name "${release_url}/gentoo-zh-verify-bot-linux-${arch}"
curl --fail --location --remote-name "${release_url}/SHA256SUMS"
sha256sum --ignore-missing --strict --check SHA256SUMS
mv "gentoo-zh-verify-bot-linux-${arch}" gentoo-zh-verify-bot
curl --fail --location \
  "https://raw.githubusercontent.com/Zakkaus/gentoo-zh-verify-bot/${version}/deploy/gentoo-zh-verify-bot.service" \
  --output gentoo-zh-verify-bot.service
```

### 从源代码构建

```sh
CGO_ENABLED=0 go build -trimpath -o gentoo-zh-verify-bot ./cmd/gentoo-zh-verify-bot
cp deploy/gentoo-zh-verify-bot.service .
```

### 安装并启动 systemd 服务

安装二进制文件和 unit。先创建权限模式为 `0600` 的空环境文件，再通过编辑器写入 `BOT_TOKEN=<your-token>`：

```sh
sudo install -Dm755 gentoo-zh-verify-bot /usr/local/bin/gentoo-zh-verify-bot
sudo install -Dm600 /dev/null /etc/gentoo-zh-verify-bot/bot.env
sudoedit /etc/gentoo-zh-verify-bot/bot.env
sudo install -Dm644 gentoo-zh-verify-bot.service /etc/systemd/system/gentoo-zh-verify-bot.service
sudo systemctl daemon-reload
sudo systemctl enable --now gentoo-zh-verify-bot
```

## 首次启动与群组登记

服务首次启动时，会将私有的一次性 owner 认领链接写入 journal。该链接默认在 10 分钟后过期；认领完成前，任何能读取 journal 的用户都可能成为 owner。`owner_claim_user_id` 可以将链接限制为一个 Telegram 用户。

```sh
sudo journalctl -u gentoo-zh-verify-bot
```

Owner 打开链接后，将机器人添加到群组并提升为管理员。机器人会登记 owner 授权的群组，并将登记状态写入 `settings.json`。随后在群组中执行 `/settings`，检查验证方式、题库和必需频道。

需要委托登记时，owner 在私聊中执行 `/enroll`，再将生成的一次性群组链接交给该群组的管理员。链接有效期为 10 分钟。没有 owner 或有效登记链接授权的未知群组会被机器人自动退出。

Owner 可以在私聊中执行 `/unregister <group-id>`。该命令只接受运行时登记的群组，会删除登记及该群组的运行时覆盖值，然后尝试退出群组。直接将机器人移出群组不会清除登记状态。

## 配置

`config.json` 可以省略；文件不存在时，机器人以零个预配置群组启动，并等待运行时登记。[`config.example.json`](config.example.json) 给出一份配置示例。文件值构成启动基线，`settings.json` 中的稀疏运行时覆盖值优先于文件，文件值优先于内置默认值。修改 `config.json` 后需要重启服务。

管理员通常只需设置以下应用环境变量：

| 变量 | 作用 |
| --- | --- |
| `BOT_TOKEN` | 必填；Telegram bot token，没有默认值。 |
| `GITHUB_TOKEN` | 可选；为 GitHub overlay 查询使用经过认证的 API 限额。 |
| `TELEGRAM_API_URL` | 可选；改用自建 Telegram Bot API server。 |

群组管理员从群内执行 `/settings`，再通过私聊面板修改以下有效设置：

- 验证开关、优先私聊、`kernel`、`quiz` 或 `mixed` 模式、申请人姓名遮盖、封禁时长、查询结果清理策略和 `lang`；
- 频道身份白名单、可信群组和已知聊天；
- 验证时限、最多失败次数和重试冷却时间；
- `questions` 选择题库、自定义 `fallback_questions` 简答题库、内置简答题，以及必需频道和加入链接。

面板可以新增、编辑和删除选择题及自定义简答题；内置简答题只能选择或恢复，不能直接编辑。`lang` 接受 `zh`、`zh-Hant` 和 `en`。机器人级设置包括 `/rich` 控制的富文本输出，以及面板中的 `private_query_per_min`；只有有效控制群组的管理员可以修改。`control_group_id` 可以显式选择控制群组；未设置时，设置存储使用第一个有效群组。

Feed 目标、overlay、新闻源、`stats_timezone` 和 `user_agent` 只能在 `config.json` 中修改。详细流程见[中文文档索引](docs/zh-CN/README.md)和[英文文档索引](docs/README.md)。

## 命令

| 范围 | 命令 | 作用 |
| --- | --- | --- |
| 已登记群组或私聊 | `/help`、`/ping`、`/stats` | 显示帮助、运行状态和统计；这些命令不占用私聊查询限额。 |
| 已登记群组或私聊 | `/pkg`、`/use`、`/arm` | 查询 Gentoo 软件包、USE flag 和 `arm64` keyword。 |
| 已登记群组或私聊 | `/bug`、`/news`、`/wiki`、`/bbs` | 查询 Gentoo Bugzilla、Gentoo 新闻、Gentoo Wiki、ArchWiki 和 Arch Linux CN。 |
| 已登记群组或私聊 | `/pkgs`、`/distro`、`/armpkgs` | 比较发行版软件包版本或 `arm64` 支持；`/distro` 是 `/pkgs` 的别名。 |
| 已登记群组的管理员 | `/mute [duration]`、`/unmute`、`/ban`、`/sb`、`/warn`、`/clearwarn` | 回复目标消息后执行禁言、封禁、清理或警告。 |
| 已登记群组的管理员 | `/start`、`/stop`、`/settings`、`/bantime`、`/bc`、`/spoiler`、`/vmode`、`/autodel` | 修改当前群组的验证、管理和消息策略；运行时值写入 `settings.json`。 |
| 控制群组的管理员 | `/rich` | 修改机器人级 `/pkg` 和 `/use` 富文本输出。 |
| Owner 私聊 | `/enroll`、`/unregister <group-id>` | 签发群组登记链接，或移除运行时登记的群组。 |

私聊中的外部查询按用户限制为每分钟 `private_query_per_min` 次，已登记群组内不限次。`/start` 还承载 owner 认领、群组登记、验证和设置面板 deep link；每类链接只能在对应的私聊或群组范围内使用。

## 状态、重启与中断

随附的 unit 通过 `StateDirectory=gentoo-zh-verify-bot` 创建权限模式为 `0700` 的 `/var/lib/gentoo-zh-verify-bot`。未设置 `$STATE_DIRECTORY` 时，普通运行状态只存在于内存，owner 认领和运行时群组登记会失败。

| 文件 | 跨重启保留的内容 |
| --- | --- |
| `settings.json` | Owner、群组登记、控制群组、一次性登记凭据，以及按群和机器人级运行时覆盖值，包括 `/bc` 状态和频道白名单。 |
| `pending.json` | 进行中的验证、模式、语言、题目、尝试次数、nonce 和截止时间。 |
| `verifyfail.json`、`agents.json`、`heartbeat.json` | 验证失败与冷却、LLM 代答陷阱累计统计，以及最近一次成功连接 Telegram 的时间。 |
| `warns.json` | 按群和用户保存的 `/warn` 计数。 |
| `feed-<chat_id>.json` | Feed 游标和已跟踪 Bugzilla 消息。 |

每日 `/stats`、设置面板会话和草稿、限流窗口、缓存、清理计时器及临时告警节流不会跨重启保留。`antispam.json` 只用于旧状态迁移，当前版本不会写入。

systemd unit 使用 `Restart=always`；除 systemd 主动停止外，进程退出 30 秒后会重启，并且没有 start-limit latch。`WatchdogSec=120s` 不是固定心跳：只有一次 `getUpdates` 调用完成后，进程才通知 watchdog；每次调用最长 45 秒。因此，正常的空轮询和失败重试都表示进程仍在取得进展，卡住的轮询则会触发 systemd 重启。

Telegram 不可达时，到期验证会获得新的完整时限，不会被拒绝或增加失败次数。运行中的中断超过 90 秒后，内存中的所有验证都会获得新的时限；重启时，如果 `heartbeat.json` 证明停机超过 90 秒，从 `pending.json` 恢复的验证也会获得新的时限。每次恢复最多尝试重新通知 30 名申请人。Telegram 只为断线机器人保留约 24 小时的 update，因此更长的中断可能丢失机器人从未收到的入群申请。恢复时，如果 `heartbeat.json` 可读，机器人会通知管理员手动检查 Telegram 的待处理申请队列。

## 为其它社区创建 fork

群组、验证模式、两种题库、三个现有 locale、overlay、新闻源、Feed 目标和消息策略都可通过配置或设置面板修改，不需要改代码。

需要更名或替换 Gentoo 语义时，必须完整修改以下位置：

- `go.mod` 的 module path 及全部 Go import；
- `cmd/gentoo-zh-verify-bot`、`deploy/gentoo-zh-verify-bot.service` 和 `.github/workflows/release.yml` 中的命令名、二进制与发布文件名、systemd 路径和状态目录；
- `internal/i18n/locales` 中的文案与内置简答题，以及 locale 注册和选择分支；
- `internal/lookup` 和 `internal/feed` 中的默认 overlay、Gentoo 数据源、查询命令和 Feed 端点；
- 文档、发布链接、安全报告地址和变更记录。

## 文档与项目政策

- [中文文档索引](docs/zh-CN/README.md)
- [英文文档索引](docs/README.md)
- [参与贡献](CONTRIBUTING.md)
- [安全政策](SECURITY.md)
- [变更记录](CHANGELOG.md)
- [MIT 许可证](LICENSE)
