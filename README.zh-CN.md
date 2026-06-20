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
- **私聊自动回复**:有人私聊机器人(非验证流程)时会收到一条**统一回复**,引导去群里使用命令,而不是没反应。可用 `private_reply` 自定义。
- **频道马甲封禁(可选)**:开启 `block_channel_senders` 后,有人在守护群里**用频道身份发言**(常见的刷屏 / 规避封禁手法)会被删消息 + 封禁该频道再也发不了。匿名群管和群的关联频道自动放行。**需要把机器人的隐私模式关掉**(BotFather → 关闭群隐私)才能看到这些消息。
- **管理命令**(回复目标消息,仅管理员):`/sb` 删消息 + 踢出(可再申请)、`/ban` 删消息 + 永久封禁、`/warn` 警告用户(满 `warn_limit` 次自动踢出,默认 3 次,计数重启不丢)、`/clearwarn` 清除某用户的警告。
- **控制 / 信息**:`/start` `/stop`(开关验证)、`/rich`(开关富文本输出)、`/ping`、`/stats`(今日通过 / 拒绝数)、`/help`。
- **包搜索**:`/pkg <名字>` 搜索官方树([packages.gentoo.org](https://packages.gentoo.org))与配置的 overlay(默认 `gentoo-zh` + `guru`),并显示版本——官方树包显示 **amd64 稳定版**,无稳定版则显示最新 `~` 测试版;overlay 包一律标 `~`。也支持完整 atom 查询(如 `/pkg sys-kernel/gentoo-kernel`)。
- **USE 标志**:`/use <包名>` 显示单个包的 USE 标志(含描述,每个都链到其 useflags 页)+ 信息。支持**包名**、**`分类/包名`** 或直接粘贴 **packages.gentoo.org(或 overlay 的 GitHub)链接**。数据取自官方树 JSON,或 overlay 的 ebuild / `metadata.xml`。
- **Bugzilla**:`/bug <编号>` 查询 [Gentoo Bugzilla](https://bugs.gentoo.org) 工单(标题 + 状态),取不到则给链接。
- **新闻**:`/news [关键词]` 列出 / 搜索 [Gentoo 新闻条目](https://www.gentoo.org/support/news-items/)。
- **Wiki 搜索**:`/wiki <关键词>` 搜索 [Gentoo](https://wiki.gentoo.org) 与 [Arch](https://wiki.archlinux.org) wiki(MediaWiki),**优先返回简体中文页**,没有则回落到默认页;其它语言的页面会被过滤掉。
- **论坛搜索**:`/bbs <关键词>` 内联返回 [Arch Linux CN](https://forum.archlinuxcn.org) 论坛(中文,走 Discourse API)的结果,并附各大英文论坛(Gentoo、Arch BBS、Ubuntu、Debian)的一键站内搜索按钮 —— 中文优先,英文备用。
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
| `group_ids` | 要守护的群(也可用单数 `group_id`) |
| `required_channel_id` | 申请人必须关注的频道数字 id;`0` 关闭 |
| `channel_display` | 展示给用户的频道,如 `@YourChannel` |
| `channel_invite_url` | 频道邀请链接;**私有频道**(无 `@` 用户名)必填 |
| `timeout_seconds` | 验证超时秒数(默认 240,上限 1800) |
| `notify_ttl_seconds` | 机器人群消息 N 秒后自动删除(`0`→60,负数→不删) |
| `warn_limit` | `/warn` 多少次后自动踢出(默认 3) |
| `admin_log_chat_id` | 可选:接收每次管理操作 / 批准失败的日志 |
| `overlays` | `/pkg` 的 GitHub overlay `[{name,repo,branch}]`(默认 gentoo-zh + guru) |
| `news_url` | `/news` 源索引 URL(默认 gentoo.org) |
| `stats_timezone` | `/stats` 每日清零所用 IANA 时区(默认 UTC+8) |
| `rich_messages` | `/pkg`、`/use` 用 Bot API 10.1 富消息(默认 `false`;也可群内 `/rich` 开关) |
| `user_agent` | 覆盖出站 HTTP User-Agent(可选;默认 `gentoo-zh-verify-bot`) |
| `private_reply` | 私聊(非验证流程)的统一自动回复(空=内置默认) |
| `block_channel_senders` | 删除并封禁群里的频道马甲发言(默认 `false`;需关隐私模式) |
| `channel_whitelist` | 上面开启时,允许在群里以频道身份发言的频道 id |
| `feed` / `feeds` | 可选:自动播报——轮询 Gentoo Bugzilla + 新闻并把新增项发到某聊天。`feed` 是单个目标;`feeds` 是它们的数组(每个有各自的聊天、语言、过滤)。见下;省略即关闭 |
| `questions` | 题库;每次随机抽一题,选项顺序打乱 |

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
| `silent_bugs` | 静默播报 bug(默认 `true`;量大,免刷通知) |

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
