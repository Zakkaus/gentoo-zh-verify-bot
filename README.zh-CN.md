# gentoo-zh-verify-bot

[English](README.md) | 简体中文

为开源社区群组打造的 Telegram **入群验证机器人**(Go,单一静态二进制,仅依赖 [telego](https://github.com/mymmrac/telego))。

有人申请入群时机器人不会直接放行:先在群内贴出验证链接 → 申请人私聊机器人答一道随机题(可选:必须已关注指定频道)→ 通过后才批准入群。管理员也能一键「直接通过」或「举报封禁」。另附轻量管理命令、Gentoo 包搜索与新闻查询。

## 功能

- **入群答题验证**:申请入群**不**自动批准。机器人在群里 @ 申请人并附「✅ 点此完成验证」深链按钮;申请人打开机器人私聊,答对一道随机单选题才被批准,答错或超时则自动拒绝。从不点击 / 不作答的广告机器人进不来。
- **频道关注门槛(可选)**:要求申请人先关注指定频道。关注这一步在**私聊里两步式**完成(先给「📢 关注频道」+「✅ 我已关注,继续」复检,再发题);群内消息**不**放频道按钮,以免把用户带离验证流程。私有频道用 `channel_invite_url` 配置邀请链接。
- **管理员一键操作**:每条申请都带「👮 直接通过」与「🚫 举报并封禁」按钮。
- **多群守护**:一个实例可同时守护多个群。
- **管理命令**(回复目标消息,仅管理员):`/sb` 删消息 + 踢出(可再申请)、`/ban` 删消息 + 永久封禁。
- **控制 / 信息**:`/start` `/stop`(开关验证)、`/ping`、`/stats`(今日通过 / 拒绝数)、`/help`。
- **包搜索**:`/pkg <名字>` 搜索官方树([packages.gentoo.org](https://packages.gentoo.org))与配置的 overlay(默认 `gentoo-zh` + `guru`),并显示版本——官方树包显示 **amd64 稳定版**,无稳定版则显示最新 `~` 测试版;overlay 包一律标 `~`。也支持完整 atom 查询(如 `/pkg sys-kernel/gentoo-kernel`)。
- **新闻**:`/news [关键词]` 列出 / 搜索 [Gentoo 新闻条目](https://www.gentoo.org/support/news-items/)。
- **重启不丢**:进行中的验证会持久化到磁盘,重启后恢复(systemd 下,见 unit 里的 `StateDirectory=`)。
- 机器人自己发的群消息在 TTL 后自动删除以保持整洁;命令显示在 Telegram 的 `/` 菜单中(管理命令仅管理员可见)。

## 部署

### 1. 创建机器人
通过 [@BotFather](https://t.me/BotFather) 创建机器人取得 token,并**关闭隐私模式**(让它能读取群消息)。

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
| `admin_log_chat_id` | 可选:接收每次管理操作 / 批准失败的日志 |
| `overlays` | `/pkg` 的 GitHub overlay `[{name,repo,branch}]`(默认 gentoo-zh + guru) |
| `news_url` | `/news` 源索引 URL(默认 gentoo.org) |
| `stats_timezone` | `/stats` 每日清零所用 IANA 时区(默认 UTC+8) |
| `questions` | 题库;每次随机抽一题,选项顺序打乱 |

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
