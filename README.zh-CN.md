# gentoo-zh-verify-bot

[English](README.md) | 简体中文

为开源社区群组打造的 Telegram **入群验证机器人**(Go,单一静态二进制,仅依赖 [telego](https://github.com/mymmrac/telego))。

有人申请入群时机器人不会直接放行:先在群内贴出验证链接 → 申请人私聊机器人答一道随机题(可选:必须已关注指定频道)→ 通过后才批准入群。管理员也能一键「直接通过」或「举报封禁」。另附轻量管理命令、Gentoo 包搜索与新闻查询。

## 功能

- **入群答题验证**:申请入群**不**自动批准。机器人在群里 @ 申请人并附「✅ 点此完成验证」深链按钮;申请人打开机器人私聊,答对一道随机单选题才被批准,答错或超时则自动拒绝。从不点击 / 不作答的广告机器人进不来。
- **频道关注门槛(可选)**:要求申请人先关注指定频道。关注这一步在**私聊里两步式**完成(先给「📢 关注频道」+「✅ 我已关注,继续」复检,再发题);群内消息**不**放频道按钮,以免把用户带离验证流程。私有频道用 `channel_invite_url` 配置邀请链接。
- **管理员一键操作**:每条申请都带「👮 直接通过」与「🚫 举报并封禁」按钮。
- **多群守护**:一个实例可同时守护多个群。
- **自动退出未授权聊天**:被加进任何不在配置里的群/频道(非守护群、非必关频道、非播报目标、非管理日志)时,机器人会立刻退出 —— 不会被人随便拉进群刷存在。要新增守护群,先把群 id 写进 `group_ids`,再把机器人加进去。
- **私聊也能用查询命令(限频)**:只读查询命令(`/pkg` `/use` `/bug` `/news` `/wiki` `/bbs` `/pkgs` `/arm` `/armpkgs`)**私聊机器人也能直接用**,每人每分钟上限 `private_query_per_min` 次(默认 3)防滥用 —— 守护群里不限次。其它私聊消息收到统一自动回复(`private_reply` 可自定义)。
- **频道马甲封禁(可选,`/bc`)**:有人在守护群里**用频道身份发言**(常见的刷屏 / 规避封禁手法)会被删消息 + 封禁该频道再也发不了。管理员用 `/bc` 开关,`/bc allow|deny <频道id>` 管白名单(`allow` 同时解封)—— 开关和白名单**重启不丢**。匿名群管和群的关联频道自动放行。**需要把机器人的隐私模式关掉**(BotFather → 关闭群隐私)才能看到这些消息。
- **管理命令**(回复目标消息,仅管理员):`/sb`、`/ban` 删消息 + 按**配置时长**封禁(`/bantime`,默认永久)、`/warn` 警告用户(满 `warn_limit` 次自动踢出,默认 3 次,计数重启不丢)、`/clearwarn` 清除某用户的警告。
- **封禁时长可配**(`/bantime`,管理员):`/bantime 0` 永久(默认)、`/bantime 7d` / `12h` / `30m` / `3600`(秒)。`/ban`、`/sb` 和验证自动封禁都用它;由 `ban_seconds` 初始化,运行时改动重启后回到配置值。
- **验证防滥用**:验证失败(答错/超时)只**拒绝**该次申请(绝不立即永久封禁),申请人需等 `verify_retry_seconds`(默认 180 秒)才能重新申请;**数小时内**连续失败 `verify_max_fails` 次(默认 3)后**自动封禁**(时长同上)。strike 计数重启不丢、验证成功后清零、且**会随时间衰减**(隔很久的偶发失误不累计)。自动封禁只有真正成功才清零;bot 无封禁权限时保留 strike 并持续告警管理员。
- **控制 / 信息**:`/start` `/stop`(开关验证)、`/rich`(开关富文本输出)、`/autodel`(开关/调节查询结果自动删除,默认 3 分钟)、`/ping`、`/stats`(今日通过 / 拒绝数)、`/help`。
- **包搜索**:`/pkg <名字>` 搜索官方树([packages.gentoo.org](https://packages.gentoo.org))与配置的 overlay(默认 `gentoo-zh` + `guru`),并显示版本——官方树包显示 **amd64 稳定版**,无稳定版则显示最新 `~` 测试版;overlay 包一律标 `~`。也支持完整 atom 查询(如 `/pkg sys-kernel/gentoo-kernel`)。
- **USE 标志**:`/use <包名>` 显示单个包的 USE 标志(含描述,每个都链到其 useflags 页)+ 信息。支持**包名**、**`分类/包名`** 或直接粘贴 **packages.gentoo.org(或 overlay 的 GitHub)链接**。数据取自官方树 JSON,或 overlay 的 ebuild / `metadata.xml`。
- **Bugzilla**:`/bug <编号>` 查询 [Gentoo Bugzilla](https://bugs.gentoo.org) 工单(标题 + 状态),取不到则给链接。
- **新闻**:`/news [关键词]` 列出 / 搜索 [Gentoo 新闻条目](https://www.gentoo.org/support/news-items/)。
- **Wiki 搜索**:`/wiki <关键词>` 搜索 [Gentoo](https://wiki.gentoo.org) 与 [Arch](https://wiki.archlinux.org) wiki(MediaWiki),**优先返回简体中文页**,没有则回落到默认页;其它语言的页面会被过滤掉。
- **论坛搜索**:`/bbs <关键词>` 内联返回 [Arch Linux CN](https://forum.archlinuxcn.org) 论坛(中文,走 Discourse API)的结果,并附各大英文论坛(Gentoo、Arch BBS、Ubuntu、Debian)的一键站内搜索按钮 —— 中文优先,英文备用。
- **arm64 状态**:`/arm <包名>` 显示一个 Gentoo 包在 **arm64 (aarch64) 上的 keyword 状态** —— 稳定、~测试、还是未 keyword —— ARM 用户一眼就能看出该包在自己架构上能不能用。
- **跨发行版 arm64**:`/armpkgs <包名>` 查该包在 **Gentoo、Debian、Ubuntu、Fedora、Arch Linux ARM、AUR** 上的 arm64 支持(各走自己的按架构 API;AUR 读 PKGBUILD 的 `arch=()`)。特别适合 Gentoo 还没给某包 keyword arm64、但别的发行版已经有的情况 —— 这通常说明它能用,可 `ACCEPT_KEYWORDS="~arm64"` 强制开启自行编译。
- **跨发行版通道标注**:Debian/Ubuntu 的发行版按**实时角色**标注(`stable`/`testing`/`oldstable`/`LTS`),取自 `distro-info-data` 而非写死 —— Debian 下次发布时"stable"会自动跟着变。**RHEL 生态拆分**为 RHEL(AlmaLinux/Rocky 1:1 重建 = 真实 RHEL 版本)、**CentOS Stream**(滚动上游)和 **EPEL**,因为它们是不同的产品。
- **跨发行版查包**:`/pkgs <包名>`(别名 `/distro`)一条消息显示一个包在 **Gentoo、AUR、Arch、Alpine、Debian、Ubuntu、Nixpkgs、Fedora、RHEL/EPEL、openSUSE(Leap + 风滚草)** 各自的当前版本(走 [Repology](https://repology.org) API),同生态的变体单独列行。每个版本都标注它来自哪个 release(如 Debian `(unstable)`、Fedora `(43)`、Alpine `(edge)`);每个发行版都链到其软件包页面;查不到精确匹配时给出最接近包的版本表 + 可折叠的其它匹配。支持 `rich_messages` / `/rich` 富文本开关(对齐 `/pkg`、`/use`)。
- **自动播报(可选)**:配置 `feed`(或用 `feeds` 数组配多个目标)后,机器人每隔 `interval_seconds`(默认 300 秒)轮询 Gentoo Bugzilla + 新闻,把**新增的** bug / 新闻发到该频道(机器人需是该频道管理员且有发帖权)。每个 feed 有各自的语言(`lang`)与过滤,所有 feed 每周期共享一次抓取。去重 + 重启不丢;首次运行只记录基线,不补发历史。
- **重启不丢**:进行中的验证会持久化到磁盘,重启后恢复(systemd 下,见 unit 里的 `StateDirectory=`)。
- **富文本输出(可选,默认关)**:`/pkg`、`/use` 可用 Bot API 10.1 富消息渲染(标题、列表、可折叠分组),由配置 `rich_messages` 或管理员 `/rich` 命令开关,失败自动回落纯 HTML。默认关闭(旧 / 第三方客户端不渲染富消息);入群验证、`/bug`、`/news` 始终用纯 HTML。
- 机器人自己发的群消息在 TTL 后自动删除以保持整洁;命令显示在 Telegram 的 `/` 菜单中(管理命令仅管理员可见)。

## 部署

### 1. 创建机器人
通过 [@BotFather](https://t.me/BotFather) 创建机器人取得 token。(隐私模式可保持**开启**:本机器人只处理命令、入群申请与按钮回调,不读取普通群消息。)

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
| `groups` | 每个群单独配置:`[{id, required_channel_id?, channel_display?, channel_invite_url?, questions?}]`。每个可选字段**缺省时回落到下面的全局默认**,所以两个群既能共享设置、也能各自独立配置。也接受裸 `group_ids` 列表(或单数 `group_id`),当作无覆盖的群 |
| `required_channel_id` | **全局默认**:申请人必须关注的频道数字 id;`0` 关闭(可在 `groups` 里按群覆盖) |
| `channel_display` | **全局默认**:展示给用户的频道,如 `@YourChannel` |
| `channel_invite_url` | **全局默认**:频道邀请链接;**私有频道**(无 `@` 用户名)必填 |
| `timeout_seconds` | 验证超时秒数(默认 240,上限 1800) |
| `notify_ttl_seconds` | 机器人群消息 N 秒后自动删除(`0`→60,负数→不删) |
| `lookup_ttl_seconds` | 查询命令(`/pkg` `/use` `/bug` `/news` `/wiki` `/bbs` `/pkgs` `/arm` `/armpkgs`)及其回复 N 秒后自动删除(不设→180=3 分钟、开;`0`/负数→关)。管理员用 `/autodel` 运行时开关/调节 |
| `warn_limit` | `/warn` 多少次后自动踢出(默认 3) |
| `private_query_per_min` | 私聊中每人每分钟可用的查询次数(默认 3;守护群不限次) |
| `ban_seconds` | `/ban`、`/sb` 和验证自动封禁的默认时长;`0` = 永久(默认)。可用 `/bantime` 运行时调整 |
| `verify_retry_seconds` | 被拒申请人需等待多久才能重新申请(默认 180;负数 = 无冷却) |
| `verify_max_fails` | 连续验证失败多少次后自动封禁(默认 3;负数 = 永不自动封禁) |
| `required_channel_fail_open` | 当 bot 读不到必关频道成员状态时,放行已答题的申请人(`true`,默认)还是拦下(`false`)。两种情况都会告警管理员 |
| `admin_log_chat_id` | 可选:接收每次管理操作 / 批准失败的日志 |
| `overlays` | `/pkg` 的 GitHub overlay `[{name,repo,branch}]`(默认 gentoo-zh + guru) |
| `news_url` | `/news` 源索引 URL(默认 gentoo.org) |
| `stats_timezone` | `/stats` 每日清零所用 IANA 时区(默认 UTC+8) |
| `rich_messages` | `/pkg`、`/use` 用 Bot API 10.1 富消息(默认 `false`;也可群内 `/rich` 开关) |
| `user_agent` | 覆盖出站 HTTP User-Agent(可选;默认 `gentoo-zh-verify-bot`) |
| `private_reply` | 私聊(非验证流程)的统一自动回复(空=内置默认) |
| `block_channel_senders` | 频道马甲封禁的**初始**状态(运行时用 `/bc` 开关,持久化;默认 `false`;需关隐私模式)。`antispam.json` 一旦生成即以它为准——之后改这个键不再生效,除非删掉该文件 |
| `channel_whitelist` | **初始**频道白名单(运行时用 `/bc allow|deny`,持久化到 `antispam.json`,该文件随后优先于此键) |
| `feed` / `feeds` | 可选:自动播报——轮询 Gentoo Bugzilla + 新闻并把新增项发到某聊天。`feed` 是单个目标;`feeds` 是它们的数组(每个有各自的聊天、语言、过滤)。见下;省略即关闭 |
| `questions` | **全局默认**题库;每次随机抽一题,选项顺序打乱(可在 `groups` 里按群覆盖) |

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
| `silent_bugs` | `true` 强制所有 bug 静默。不设时:**未确认(UNCONFIRMED)bug 静默推送**(可能误报),已确认 bug 带通知 |

### 4. 构建运行
需要 **Go 1.25+**(telego 要求)。

```sh
CGO_ENABLED=0 go build -o /usr/local/bin/gentoo-zh-verify-bot .
sudo cp deploy/gentoo-zh-verify-bot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now gentoo-zh-verify-bot
journalctl -fu gentoo-zh-verify-bot
```

采用长轮询(long polling),无需开放入站端口或反向代理。

## 说明 / 限制
- 每日 `/stats` 统计在内存中,重启清零;**进行中的验证**会持久化到 `$STATE_DIRECTORY/pending.json`,在 systemd 下(unit 里的 `StateDirectory=`)重启后恢复定时器。
- 验证链接依赖群为**公开群**。
- 界面文案为**简体中文**(本项目面向 Gentoo 中文社区)。运营设置全在配置文件;要改语言需修改 `.go` 源码中的字符串字面量(主要在 `verify.go`、`admin.go`、`commands.go`)。

## 许可证
MIT — 见 [LICENSE](LICENSE)。
