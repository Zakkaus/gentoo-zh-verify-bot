# 部署

本文说明首次启动、持久 owner 认领、运行时群组注册、权限诊断和仓库提供的 systemd unit。配置项定义不在此重复。

## 只提供 `BOT_TOKEN` 的首次启动

**实现位置：**`main` 包；`cmd/gentoo-zh-verify-bot/main.go` 中的 `main` 和 `loadRuntimeState`；`internal/config` 包；`internal/config/config.go` 中的 `LoadConfig`；`internal/store` 包；`internal/store/baseline.go` 中的 `LoadBaseline` 和 `EffectiveConfig`。

除 `-version` 外，应用只强制要求 `BOT_TOKEN`。默认配置路径为 `/etc/gentoo-zh-verify-bot/config.json`。文件不存在时按 `{}` 处理，以零个已配置群组启动。现有文件不可读、JSON 损坏、群组、模式、题目、频道或 baseline 无效时，启动失败。未知 key 只记录警告。

`STATE_DIRECTORY` 非空时，`loadRuntimeState` 尝试以 `0700` 创建目录，清理遗留的 `.<name>.tmp-*`，并把 `settings.json` 放在该目录中。创建失败只记录警告，启动继续，之后的持久化可能失败。变量为空时，普通设置只存在于内存，验证、警告和 feed 状态都不能跨重启保存。Owner 认领和运行时群组注册要求更严格：没有持久设置存储时直接拒绝。

随后，程序创建 Bot API 客户端，以五秒间隔配置 long polling 重试，创建处理器，并强制执行 `GetMe`。机器人创建、首次 long polling、处理器创建或启动、`GetMe` 失败都会结束进程。Owner 和注册路由先于普通应用路由注册。程序再启动异步权限检查、注册命令菜单、启动可选 feed、查询缓存预热和 heartbeat，最后运行处理器。更新流在没有停止信号时结束，进程以非零状态退出，交由 systemd 重启。

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

## systemd unit 和状态目录

**实现位置：**`main` 包；`cmd/gentoo-zh-verify-bot/main.go` 中的 `main`；部署定义 `deploy/gentoo-zh-verify-bot.service`。

仓库提供的 unit：

- 执行 `/usr/local/bin/gentoo-zh-verify-bot --config /etc/gentoo-zh-verify-bot/config.json`；
- 读取 `/etc/gentoo-zh-verify-bot/bot.env`；
- 使用 `DynamicUser=yes`；
- 创建 `/var/lib/gentoo-zh-verify-bot`，以 `0700` 作为 `STATE_DIRECTORY`；
- 只在失败时重启，间隔五秒；
- 允许出站 `AF_UNIX`、IPv4 和 IPv6，不监听端口；
- 应用 `MemoryMax=512M`、`UMask=0077`、空 capability 集、文件系统和内核及进程防护，以及 `@system-service` 系统调用过滤。

收到 SIGINT 或 SIGTERM 后，程序停止处理器，冻结验证计时器，保存验证状态，并最多等待 feed 五秒完成写入。致命启动或处理器错误使用 `log.Fatal` 或 `log.Fatalf`，不会执行 defer 中的优雅停止；恢复依赖运行过程中最后成功写入的状态。

Systemd 会在启动应用前创建状态目录。手动运行时，如果状态和 owner 注册必须跨重启保存，应把 `STATE_DIRECTORY` 指向私有且可写的目录。各文件定义见[状态与持久化](state-persistence.md)。
