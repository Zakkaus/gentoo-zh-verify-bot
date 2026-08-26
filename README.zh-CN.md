# gentoo-zh-verify-bot

[English](README.md) | 简体中文

面向 Linux 社区群组的 Telegram 验证机器人，用于筛除批量提交入群申请的垃圾账号，由 [Gentoo 中文社区](https://gentoozh.org)开发并运行。申请入群的人在通过或失败之前保持待审；进入未开启审批的群组的人先被禁言，直到完成验证。同时提供群组管理、跨发行版的软件包与文档查询，以及可选的缺陷与新闻推送。

## 两个版本

同一份代码，两个二进制。区别只在于不带前缀的命令名归谁使用。

| | 面向 | Gentoo 查询命令 |
| --- | --- | --- |
| `gentoo-zh-verify-bot` | Gentoo 中文社区 | `/pkg` `/use` `/bug` `/news` `/bbs` `/arm` |
| `gentoo-zhbot` | 一般 Linux 社区 | `/gpkg` `/guse` `/gbug` `/gnews` `/gbbs` `/garm` |

其余完全相同，包括验证、群组管理、设置面板，以及所有 Linux 社区共用的查询：`/pkgs` `/distro` `/armpkgs` `/wiki` `/kernel` `/man` `/cve` `/repology`。运行 Arch 或 Debian 的群组因此保留了 `/pkg` 这个名字，同时仍然可以在需要时查询 Gentoo。

Gentoo 版用 `-tags gentoo` 构建，默认构建即通用版。每次发布同时提供两者的 `amd64` 与 `arm64` 二进制。

## 适用条件与运行开销

机器人适用于启用入群申请的 Telegram 群组或超级群组。机器人必须是群组管理员，并具有**邀请用户**、**封禁用户**和**删除消息**权限；如果启用必需频道条件，还必须是该频道的管理员。只有 `/bc` 需要关闭 BotFather 隐私模式。

发布文件是面向 Linux `amd64` 和 `arm64` 的静态链接二进制文件。使用 Telegram 托管 Bot API 和默认数据源时，机器人只需要出站 HTTPS，不需要数据库、反向代理或入站端口。随附的 systemd unit 将持久状态放在私有目录中，并通过 `MemoryMax=512M` 设置内存上限；`512M` 是安全边界，不是常驻内存用量。

## 验证流程

每项入群申请都按群组独立处理。按群设置 `delivery_mode` 默认为 `both`：机器人先发送群内验证消息，其中的 `verify_<groupID>` deep link 只打开该群组的待验证申请，再尝试发送私聊验证题。任一消息确认送达后，程序开始完整答题时限。

`group` 只发送群内验证消息。`dm` 先尝试私聊，仅在 Telegram 明确拒绝时发送群内验证消息。传输错误或 5xx 可能发生在 Telegram 已接收私聊消息之后，因此 `dm` 在投递状态不确定时不会回退；`both` 不会发送第二条群内消息。

| 模式 | 申请人操作 | 行为 |
| --- | --- | --- |
| `kernel`（默认） | 输入正在运行的 Linux 内核版本；可执行 `uname -r` 获取 | 最多输入 3 次。没有 Linux 设备时，申请人须先声明该情况并提供当前分钟数，再回答不显示答案的简答题。 |
| `quiz` | 点击题库中随机打乱后的正确选项 | 使用该群组的 `questions`；题库为空时回退到 `kernel`。 |
| `mixed` | 完成随机选中的 `kernel` 或 `quiz` | 每项申请独立选择；题库为空时使用 `kernel`。 |

`kernel` 和简答题提示包含按申请变化的 LLM 代答陷阱。完全遵循隐藏指令会导致验证失败；对方声明的模型只用于累计统计，因此该机制用于威慑，不是安全边界。

群组可以让可信群组的成员免验证，也可以要求申请人在批准前加入指定频道。答错或超时会拒绝申请并进入冷却期；默认在 6 小时计数窗口内失败 3 次会触发自动封禁。申请人文案根据 Telegram `language_code` 使用简体中文、繁体中文或英文；群组和管理员文案使用该群组的 `lang`。

## 安装

需要你自己提供的只有 `BOT_TOKEN`。安装脚本会按本机架构下载发布二进制、对照官方 `SHA256SUMS` 校验、安装 systemd 单元并启用服务。再次运行即为原地升级，不会覆盖 `bot.env`。

```sh
curl --fail --location --remote-name \
  https://raw.githubusercontent.com/Zakkaus/gentoo-zh-verify-bot/main/deploy/install.sh
sh install.sh                       # 或指定版本：sh install.sh v4.3.0
sudoedit /etc/gentoo-zh-verify-bot/bot.env   # 填入 BOT_TOKEN=<@BotFather 给的令牌>
sudo systemctl start gentoo-zh-verify-bot
```

运行前请先读一遍脚本，它很短，做的就是上面这几件事。

### 改为从源代码构建

需要 Go 1.26.7 或更高版本，单元文件与路径与上面相同。

```sh
CGO_ENABLED=0 go build -trimpath -o gentoo-zh-verify-bot ./cmd/gentoo-zh-verify-bot
sudo install -Dm755 gentoo-zh-verify-bot /usr/local/bin/gentoo-zh-verify-bot
sudo install -Dm644 deploy/gentoo-zh-verify-bot.service /etc/systemd/system/
sudo install -Dm600 /dev/null /etc/gentoo-zh-verify-bot/bot.env
sudoedit /etc/gentoo-zh-verify-bot/bot.env
sudo systemctl daemon-reload
sudo systemctl enable --now gentoo-zh-verify-bot
```

## 首次启动与群组登记

服务首次启动时，会将私有的一次性 owner 认领链接写入 journal。该链接默认在 10 分钟后过期；认领完成前，任何能读取 journal 的用户都可能成为 owner。`owner_claim_user_id` 可以将链接限制为一个 Telegram 用户。

```sh
sudo journalctl -u gentoo-zh-verify-bot
```

Owner 认领成功后，程序立即刷新私聊命令菜单，其中包含成员命令以及 `/enroll` 和 `/unregister`。随后将机器人添加到群组并提升为管理员。机器人会登记 owner 授权的群组，并将登记状态写入 `settings.json`。在群组中执行 `/settings`，检查验证方式、验证题发送方式、题库和必需频道。

需要委托登记时，owner 在私聊中执行 `/enroll`，再将生成的一次性群组链接交给该群组的管理员。链接有效期为 10 分钟。没有 owner 或有效登记链接授权的未知群组会被机器人自动退出。

Owner 可以在私聊中执行 `/unregister <group-id>`。该命令只接受运行时登记的群组，会删除登记及该群组的运行时覆盖值，然后尝试退出群组。直接将机器人移出群组不会清除登记状态。

## 配置

`config.json` 是可选的，多数部署根本不需要：群组在运行时添加，几乎所有设置都能在 `/settings` 中修改且无需重启。[`config.example.json`](config.example.json) 只是一个两行的起点，[配置参考](docs/zh-CN/configuration.md)列出了每个字段的默认值，并注明哪些已经由设置面板覆盖。

取值顺序只有一种：`settings.json` 中的运行时覆盖优先，其次是 `config.json`，最后是内置默认值。修改 `config.json` 需要重启，面板不需要。

环境变量共三个。`BOT_TOKEN` 必填；`GITHUB_TOKEN` 提高 overlay 查询使用的 GitHub API 配额；`TELEGRAM_API_URL` 指向自建的 Bot API 服务器。

## 命令

| 范围 | 命令 | 作用 |
| --- | --- | --- |
| 已登记群组或私聊 | `/help`、`/ping`、`/stats` | 显示帮助、运行状态和统计；这些命令不占用私聊查询限额。 |
| 已登记群组或私聊 | `/pkg`、`/use`、`/arm` | 查询 Gentoo 软件包、USE flag 和 `arm64` keyword。 |
| 已登记群组或私聊 | `/bug`、`/news`、`/wiki`、`/bbs` | 查询 Gentoo Bugzilla、Gentoo 新闻、Gentoo Wiki、ArchWiki 和 Arch Linux CN。 |
| 已登记群组或私聊 | `/pkgs`、`/distro`、`/armpkgs` | 比较发行版软件包版本或 `arm64` 支持；`/distro` 是 `/pkgs` 的别名。 |
| 已登记群组或私聊 | `/kernel`、`/man`、`/cve`、`/repology` | 查询 kernel.org 发布的内核版本、Linux 手册页、CVE 漏洞编号，以及软件包在各发行版仓库中的版本。 |
| 已登记群组的管理员 | `/mute [duration]`、`/unmute`、`/ban`、`/sb`、`/warn`、`/clearwarn` | 回复目标消息后执行禁言、封禁、清理或警告。 |
| 已登记群组的管理员 | `/start`、`/stop`、`/settings`、`/bantime`、`/bc`、`/spoiler`、`/vmode`、`/autodel` | 修改当前群组的验证、管理和消息策略；运行时值写入 `settings.json`。 |
| 控制群组的管理员 | `/rich` | 修改机器人级 `/pkg` 和 `/use` 富文本输出。 |
| Owner 私聊 | `/enroll`、`/unregister <group-id>` | 签发群组登记链接，或移除运行时登记的群组。 |
上表用的是 Gentoo 版的命令名。通用版 `gentoo-zhbot` 把六条 Gentoo 查询改为 `/gpkg`、`/guse`、`/gbug`、`/gnews`、`/gbbs`、`/garm`，其余命令名称相同，`/rich` 在通用版控制的是 `/gpkg` 和 `/guse`。

私聊中的外部查询按用户限制为每分钟 `private_query_per_min` 次，已登记群组内不限次。`/start` 还承载 owner 认领、群组登记、验证和设置面板 deep link；每类链接只能在对应的私聊或群组范围内使用。

## 状态、重启与中断

随附的 unit 通过 `StateDirectory=gentoo-zh-verify-bot` 创建权限模式为 `0700` 的 `/var/lib/gentoo-zh-verify-bot`。未设置 `$STATE_DIRECTORY` 时，普通运行状态只存在于内存，owner 认领和运行时群组登记会失败。

| 文件 | 跨重启保留的内容 |
| --- | --- |
| `settings.json` | Owner、群组登记、控制群组、一次性登记凭据，以及按群和机器人级运行时覆盖值，包括 `/bc` 状态和频道白名单。 |
| `pending.json` | 进行中的验证、送达状态、群内与私聊验证消息 ID、模式、语言、题目、尝试次数、nonce 和截止时间。 |
| `verifyfail.json`、`agents.json`、`heartbeat.json` | 验证失败与冷却、LLM 代答陷阱累计统计，以及最近一次成功连接 Telegram 的时间。 |
| `warns.json` | 按群和用户保存的 `/warn` 计数。 |
| `feed-<chat_id>.json` | Feed 游标和已跟踪 Bugzilla 消息。 |

每日 `/stats`、设置面板会话和草稿、限流窗口、缓存、清理计时器及临时告警节流不会跨重启保留。`antispam.json` 只用于旧状态迁移，当前版本不会写入。

systemd unit 使用 `Restart=always`；除 systemd 主动停止外，进程退出 30 秒后会重启，并且没有 start-limit latch。`WatchdogSec=120s` 不是固定心跳：只有一次 `getUpdates` 调用完成后，进程才通知 watchdog；每次调用最长 45 秒。因此，正常的空轮询和失败重试都表示进程仍在取得进展，卡住的轮询则会触发 systemd 重启。

Telegram 不可达时，到期验证会获得新的完整时限，不会被拒绝或增加失败次数。运行中的中断超过 90 秒后，内存中的所有验证都会获得新的时限；重启时，如果 `heartbeat.json` 证明停机超过 90 秒，从 `pending.json` 恢复的验证也会获得新的时限。每次恢复最多尝试重新通知 30 名申请人。Telegram 只为断线机器人保留约 24 小时的 update，因此更长的中断可能丢失机器人从未收到的入群申请。恢复时，如果 `heartbeat.json` 可读，机器人会通知管理员手动检查 Telegram 的待处理申请队列。

## 适配到其它社区

多数社区不需要 fork：运行 `gentoo-zhbot` 版本再配置即可。群组、验证模式、两种题库、三个现有 locale、overlay、新闻源、推送目标和消息策略都可通过 `config.json` 或设置面板修改，不需要改代码。

若要彻底替换 Gentoo 语义，而不是让它保留在 `g` 前缀之后，必须完整修改以下位置：

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
