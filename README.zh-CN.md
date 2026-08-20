# gentoo-zh-verify-bot

[English](README.md) | 简体中文

为开源社区群组打造的 Telegram **入群验证机器人**(Go,单一静态二进制,仅依赖 [telego](https://github.com/mymmrac/telego))。

有人申请入群时机器人不会直接放行:先在群内贴出验证链接 → 申请人私聊机器人完成验证,默认要求**自己填写正在运行的 Linux 内核版本号**(可选:必须已关注指定频道)→ 通过后才批准入群。管理员也能一键「直接通过」或「举报封禁」。另附轻量管理命令、Gentoo 包搜索与新闻查询。

## 功能

**入群验证** —— 申请入群**不**自动批准。机器人在群里 @ 申请人并附「✅ 完成验证」深链;申请人打开机器人,在私聊里完成验证 —— 可选先关注**必关频道**(私聊两步引导;私有频道用 `channel_invite_url`)—— 通过才批准,答错/超时则拒绝。每条申请还带「👮 直接通过」/「🚫 举报并封禁」按钮。

- **验证方式**(`verify_mode`,管理员可用 `/vmode` 随时切换):

  | 模式 | 申请人要做的事 | 说明 |
  | --- | --- | --- |
  | `kernel`(默认) | **手动填写自己正在运行的 Linux 内核版本号**(`uname -r`) | 没有按钮可点,只会乱点的诈骗机器人填不出合理的版本号。任何发行版的内核号都接受:历史版本(`2.6.32`)、当前版本(`6.18.45`)、以后的新主版本(`8.0`),以及各种带后缀的串:`5.14.0-570.12.1.el9_6.x86_64`、`5.15.167.4-microsoft-standard-WSL2`,**ARM 同样通过** —— `6.6.51+rpt-rpi-v8`(树莓派)、`5.10.110-tegra`(Jetson)、`6.1.75-android14-11-g1c2d3e4f`(安卓 / Termux)、`6.11.0-asahi-…`(Apple Silicon)、`…el9.aarch64`;整段 `uname -a` 粘进来也认。共 3 次机会,打错一次不会立即被拒。回复「还没装」「不用 Linux」「用的 Windows」的人换成一道**简答题**(题库 `fallback_questions`,例如「Gentoo 中文社区的官网网址是什么?」→ `gentoozh.org`):要手动打字、没有选项、没用过 Linux 也答得出、**答案不出现在题面里**,所以看到这条路也捞不到答案。这条路不会写在题目里主动告诉人,另外把我们自己印的格式示例原样发回来会被退回一次,不会直接算过 |
  | `quiz` | 从 `questions` 里随机抽一题(选项 crypto 打乱),点对应按钮 | 原有方式;成员不全是 Linux 用户的群可以继续用 |
  | `mixed` | 每个申请人随机拿到上面两种之一 | |

- **AI 代答陷阱 + 模型统计**:私聊消息里带一句只对自动化程序说的话 —— 要求代人答题的 LLM 智能体不要作答,改回复「每人不同的令牌 + 自己的模型名」(`AGENT-… model=gpt-5-mini`)。照做的智能体等于自报身份:当场拒绝,同时把它自称的模型记进 `agents.json`,`/stats` 显示 `🤖 拦截 AI 代答:12 次(gpt-5 5、claude-opus-4.5 4、unknown 3)`,管理日志每次也有一条。模型是对方自己报的,只能当使用统计,不能当证据。这是威慑手段,**不是**安全边界,真正拦人的仍是「必须手动打字」加上超时、冷却与失败计数。那句提示装在折叠引用里(`<blockquote expandable>`,Bot API 7.4):旧客户端只是不折叠、照常显示;万一自建的旧版 Bot API server 不认这个实体,私聊会自动改发不带折叠的版本,再不行发纯文本,不会让申请人收不到题。
- **三种语言,按申请人自动选**:入群提示、私聊题目、关注频道引导、通过/拒绝结果,全部按申请人 Telegram 的界面语言(`language_code`)渲染 —— 简体中文、繁体中文(`zh-TW`/`zh-HK`/`zh-Hant`),其余语言一律英文。管理端输出和管理日志保持简体中文。选择题题库 `questions` 来自配置文件,**不会**被翻译,这也是混合语言群建议用内核模式的原因。
- **防滥用**:验证失败先**拒绝**并冷却(`verify_retry_seconds`,180 秒);数小时内连续失败 `verify_max_fails`(默认 3)次后自动封禁。strike 持久化、成功清零、随时间衰减。

**管理命令**(管理员,回复目标消息):

| 命令 | 作用 |
| --- | --- |
| `/mute [时长]` · `/unmute` | 禁言 —— 留群但不能发言;限时(默认 1 小时,如 `/mute 30m`);`/unmute` 提前解除 |
| `/ban` | 封禁 —— 踢出群;时长见 `/bantime`(默认永久,或限时=到期可重进) |
| `/sb` | 举报并封禁 —— 同 `/ban`,再**清除该用户全部消息** |
| `/warn` · `/clearwarn` | 警告(满 `warn_limit` 次自动踢,默认 3) · 清除警告 |
| `/bantime` | 设定封禁时长:`0`=永久,或 `7d`/`12h`/`30m` |
| `/bc` | 频道马甲封禁 + 白名单(需关隐私模式;持久化) |

**Gentoo / Linux 查询**(私聊也能用,每分钟限 `private_query_per_min` 次):

| 命令 | 查询 |
| --- | --- |
| `/pkg <名字>` | Gentoo 包 + 版本(官方树 + `gentoo-zh`/`guru` overlay) |
| `/use <包名>` | 某个包的 USE 标志 + 信息 |
| `/bug <编号>` | Gentoo Bugzilla 工单 |
| `/news [关键词]` | Gentoo 新闻 |
| `/wiki <关键词>` | Gentoo / Arch wiki(优先简体中文页) |
| `/bbs <关键词>` | Linux 论坛(Arch Linux CN 内联 + 英文论坛按钮) |
| `/pkgs <包名>` | 跨发行版版本(走 [Repology](https://repology.org),按 release 标注;RHEL ≠ CentOS Stream ≠ EPEL) |
| `/arm <包名>` | 某 Gentoo 包的 arm64 keyword 状态 |
| `/armpkgs <包名>` | 跨发行版 arm64 支持(Gentoo/Debian/Ubuntu/Fedora/Arch ARM/AUR) |

**自动播报(可选)** —— 轮询 Gentoo Bugzilla + 新闻,把**新增**项发到一个或多个频道(`feed` / `feeds`),各有语言 + 过滤;去重、重启不丢,**bug 状态变化时就地编辑那条消息**(未确认→已确认时就地编辑并补发一条 🔔 通知——因为未确认时的原消息是静默的;解决时 🐞→✅)。

**其它**:守护多个群;自动退出未授权聊天;验证进度重启不丢;群消息按 TTL 自动删除;**默认把新成员的名字用防剧透遮盖**,防止广告号拿名字刷屏(`/spoiler`,可持久化);`/pkg` `/use` 可选富文本(`rich_messages` / `/rich`,默认关);`/ping` `/stats` `/start` `/stop` `/autodel` `/rich` `/spoiler` `/vmode` `/help`。

## 部署

### 1. 创建机器人
通过 [@BotFather](https://t.me/BotFather) 创建机器人取得 token。(隐私模式默认可保持**开启**:验证、命令、按钮回调都正常;**仅当**要用频道马甲封禁 `/bc` 时,才需在 BotFather 关闭群隐私,否则机器人收不到「以频道身份发的」消息。)

### 2. 群 / 频道设置
- 把机器人加入每个群并设为**管理员**,授予三项权限:**批准新成员**、**封禁用户**、**删除消息**。
- 每个群须为**公开群**(待审申请人才看得到验证链接),并开启**「新成员需管理员批准」**(join-by-request)。
- (可选频道门槛)把机器人加入指定频道并设为**管理员**——`getChatMember` 只有在机器人是该频道管理员时,才能可靠查询他人是否已关注。

> 获取数字 chat id(`-100…` 形式):转发任意一条消息给 [@userinfobot](https://t.me/userinfobot) / [@JsonDumpBot](https://t.me/JsonDumpBot),或查看本机器人日志。

### 3. 配置
token 走环境变量(**切勿提交进仓库**):

```sh
# /etc/gentoo-zh-verify-bot/bot.env   (chmod 600)
BOT_TOKEN=123456:ABC-DEF...
# 可选:一个 GitHub token(无需任何权限/scope)把 /pkg 抓 overlay 的 API 限额
# 从 60/h 提到约 5000/h,这样可以安全地多配几个 overlay。
GITHUB_TOKEN=ghp_xxx
```

其余配置写在 `config.json`(复制 `config.example.json` 修改):

| 键 | 含义 |
| --- | --- |
| `groups` | 每个群单独配置:`[{id, required_channel_id?, channel_display?, channel_invite_url?, trusted_member_group_ids?, questions?, verify_mode?}]`。每个可选字段**缺省时回落到下面的全局默认**,所以两个群既能共享设置、也能各自独立配置。也接受裸 `group_ids` 列表(或单数 `group_id`),当作无覆盖的群 |
| `required_channel_id` | **全局默认**:申请人必须关注的频道数字 id;`0` 关闭(可在 `groups` 里按群覆盖) |
| `channel_display` | **全局默认**:展示给用户的频道,如 `@YourChannel` |
| `channel_invite_url` | **全局默认**:频道邀请链接;**私有频道**(无 `@` 用户名)必填 |
| `timeout_seconds` | 验证超时秒数(默认 240,上限 1800) |
| `notify_ttl_seconds` | 机器人群消息 N 秒后自动删除(`0`→60,负数→不删) |
| `lookup_ttl_seconds` | 查询命令(`/pkg` `/use` `/bug` `/news` `/wiki` `/bbs` `/pkgs` `/arm` `/armpkgs`)及其回复 N 秒后自动删除(不设→180=3 分钟、开;`0`/负数→关)。管理员用 `/autodel` 运行时开关/调节 |
| `warn_limit` | `/warn` 多少次后自动踢出(默认 3) |
| `private_query_per_min` | 私聊中每人每分钟可用的查询次数(默认 3;守护群不限次) |
| `ban_seconds` | `/ban`、`/sb` 和验证自动封禁的默认时长;`0` = 永久(默认)。可用 `/bantime` 运行时调整 |
| `mute_seconds` | `/mute`(禁言)默认时长;用户留在群里但不能发言,到期自动解除(默认 3600 = 1 小时;始终限时)。可在命令后指定,如 `/mute 30m`;`/unmute` 提前解除 |
| `verify_retry_seconds` | 被拒申请人需等待多久才能重新申请(默认 180;负数 = 无冷却) |
| `verify_max_fails` | 连续验证失败多少次后自动封禁(默认 3;负数 = 永不自动封禁) |
| `required_channel_fail_open` | 当 bot 读不到必关频道成员状态时,放行已答题的申请人(`true`,默认)还是拦下(`false`)。两种情况都会告警管理员 |
| `trusted_member_group_ids` | **可信成员免验证**:申请人若**已经是这些群之一的成员**,直接批准、跳过答题(例:子群信任主群的成员)。**全局默认;按群**:省略=继承全局,写 `[]`=本群**关闭**,写 id 列表=覆盖全局。用真实 chat id(群是 `-100…`);bot 必须在每个列出的群里才能读成员状态(当作已知聊天、不会被自动退出)。可信成员**优先于失败冷却**(即使之前验证失败被冷却,也会被直接放行并清空 strike)。与必关频道不同,这里**fail-closed**——读不到成员身份就走正常验证,绝不直接放行 |
| `admin_log_chat_id` | 可选:接收每次管理操作 / 批准失败的日志 |
| `overlays` | `/pkg` 的 GitHub overlay `[{name,repo,branch}]`(默认 gentoo-zh + guru) |
| `news_url` | `/news` 源索引 URL(默认 gentoo.org) |
| `stats_timezone` | `/stats` 每日清零所用 IANA 时区(默认 UTC+8) |
| `rich_messages` | `/pkg`、`/use` 用 Bot API 10.1 富消息(默认 `false`;也可群内 `/rich` 开关) |
| `user_agent` | 覆盖出站 HTTP User-Agent(可选;默认 `gentoo-zh-verify-bot`) |
| `private_reply` | 私聊(非验证流程)的统一自动回复(空=内置默认) |
| `block_channel_senders` | 频道马甲封禁的**初始**状态(运行时用 `/bc` 开关,持久化;默认 `false`;需关隐私模式)。`antispam.json` 一旦生成即以它为准——之后改这个键不再生效,除非删掉该文件 |
| `channel_whitelist` | **初始**频道白名单(运行时用 `/bc allow` / `deny`,持久化到 `antispam.json`,该文件随后优先于此键) |
| `feed` / `feeds` | 可选:自动播报——轮询 Gentoo Bugzilla + 新闻并把新增项发到某聊天。`feed` 是单个目标;`feeds` 是它们的数组(每个有各自的聊天、语言、过滤)。见下;省略即关闭 |
| `verify_mode` | 验证方式:`kernel`(默认,填 `uname -r` 的内核版本号)、`quiz`(点选项)或 `mixed`(每人随机)。**全局默认,可在 `groups` 里按群覆盖**;管理员用 `/vmode kernel\|quiz\|mixed\|auto` 实时切换(可持久化,`auto` 表示回到配置文件) |
| `fallback_questions` | 覆盖内置简答题库(申请人说自己没装 Linux 时用):`[{q, answers:[…]}]`,答案按整词、不分大小写匹配。**答案不要写进题面**。留空则用内置的三语题库 |
| `questions` | **全局默认**题库;每次随机抽一题,选项顺序打乱(可在 `groups` 里按群覆盖)。只有会出选择题的群才需要;纯 `kernel` 模式可以不写 |

可选的 **`feed`** 对象——或 **`feeds`**(这些对象的数组,配多个目标,每周期共享一次抓取)。两者都省略即关闭:

| `feed` 键 | 含义 |
| --- | --- |
| `chat_id` | 发送目标频道/群(`0`/缺省关闭;机器人须是该频道管理员且有发帖权) |
| `lang` | bug 字段标签语言:`zh`(默认)或 `en` |
| `interval_seconds` | 轮询间隔(默认 300,最小 60) |
| `bugs` | 是否播报新 Bugzilla bug(默认 `true`) |
| `news` | 是否播报新新闻(默认 `true`) |
| `bug_product` | 只播报该 Bugzilla 产品的 bug,如 `"Gentoo Security"`(空=全部) |
| `bug_component` | 只播报该组件的 bug,如 `"Vulnerabilities"`(空=全部) |
| `silent_bugs` | `true` 强制所有 bug 静默。不设时:**未确认(UNCONFIRMED)bug 静默推送**(可能误报),已确认 bug 带通知;静默的未确认 bug 之后变为已确认时,会补发一条 🔔 提示(`silent_bugs` 为 `true` 时不补发) |

### 4. 构建运行
需要 **Go 1.26.6+**(与 `go.mod` 一致;1.26.6 含安全修复)。

> **安装:** 可从 [Releases](https://github.com/Zakkaus/gentoo-zh-verify-bot/releases) 下载预编译的
> `linux-amd64`/`arm64` 静态二进制(附 `SHA256SUMS`),或按下面从源码构建。注意 `go install …@v3.x`
> **不可用**(module path 没有 `/vN` 大版本后缀 —— 这是二进制而非被引用的库,属预期);请 clone 后
> `go build`,或直接用 release 二进制。

```sh
CGO_ENABLED=0 go build -o /usr/local/bin/gentoo-zh-verify-bot .
sudo cp deploy/gentoo-zh-verify-bot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now gentoo-zh-verify-bot
journalctl -fu gentoo-zh-verify-bot
```

采用长轮询(long polling),无需开放入站端口或反向代理。

## 说明 / 限制
- **状态持久化。** 在 systemd 下(unit 的 `StateDirectory=` 会设置 `$STATE_DIRECTORY`),机器人持久化下列状态并在重启后重新载入;若 `STATE_DIRECTORY` 未设置,则**一律不持久化**——全部仅存于内存,重启即丢(会打日志告警)。状态目录必须**仅对机器人的服务用户私有**、不可被不可信用户写入(unit 的 `StateDirectory=` + `DynamicUser=` 已保证这点)。

  | 持久化(`$STATE_DIRECTORY/…`) | 内容 |
  | --- | --- |
  | `pending.json` | 进行中的验证(重启后重新武装定时器) |
  | `warns.json` | 每用户 `/warn` 警告计数 |
  | `antispam.json` | `/bc` 频道马甲状态 + 白名单 |
  | `verifyfail.json` | 验证失败 strike / 冷却 |
  | `feed-<chat_id>.json` | 播报去重游标 + 已跟踪 bug 的消息 id |
  | `settings.json` | 验证开关状态(`/start` · `/stop`)、名字遮盖开关(`/spoiler`)和验证方式(`/vmode`)—— 均可跨重启保留 |
  | `agents.json` | AI 代答陷阱命中次数,按自称模型分类(`/stats` 显示) |

  **不**持久化(重启清零):每日 `/stats`;`/rich`、`/autodel`、`/bantime` 的运行时改动;以及查询 / 新闻 / 包缓存。
- 验证链接依赖群为**公开群**。
- 管理指令需**以个人身份**发送 —— 匿名管理员发言显示为「群」而非用户,过不了管理员校验。
- 多个守护群若**必关频道不同**,私聊关注引导只覆盖第一个待处理群的频道;共用一个频道体验最顺。
- **入群验证**的文案支持简体中文、繁体中文与英文,按申请人 Telegram 的 `language_code` 自动选择(见 `i18n.go`)。其余部分 —— 管理回复、`/help`、管理日志、查询命令 —— 仍是**简体中文**(本项目面向 Gentoo 中文社区),要改需修改 `.go` 源码中的字符串字面量(主要在 `verify.go`、`admin.go`、`commands.go`)。选择题题库 `questions` 按配置原样使用,不做翻译。

## 许可证
MIT — 见 [LICENSE](LICENSE)。
