# 状态与持久化

所有持久运行状态均为 `STATE_DIRECTORY` 下的 JSON。应用不会写入 `config.json`。

## 存储事务

**实现位置：**`internal/store` 包；`internal/store/json.go` 中的 `Load`、`Save`、`Write`、`writeLocked` 和 `ReclaimTemps`；`main` 包；`cmd/gentoo-zh-verify-bot/main.go` 中的 `loadRuntimeState`。

所有状态写入共用一把进程级 mutex。存储层先序列化稳定快照，在同目录创建权限为 `0600` 的临时文件，写入并执行 `fsync`，关闭文件，原子重命名为目标，再对父目录执行 `fsync`。父目录同步失败时，重命名可能已经完成，但调用者仍会收到错误。启动过程会从 `STATE_DIRECTORY` 清理符合 `.<name>.tmp-*` 的遗留临时文件。

文件不存在是正常首次状态。JSON 解码失败时，程序记录日志，并尝试把原文件改名为 `<name>.corrupt`；代码没有这些备份的保留或清理策略。重命名失败时，原文件留在原路径，`Load` 返回停用写入的专用错误，避免调用者覆盖该文件。现有文件不可读时也返回同类错误。未知 JSON 字段可以读入，但后续重写时会丢失。

多数状态生产者只记录并忽略写入错误，因此内存行为继续，下一次普通状态变更可能再次写入。设置提交例外：写入失败时不会发布候选快照。

## `settings.json`

**实现位置：**`internal/store` 包；`internal/store/settings.go` 中的 `NewSettings`、`(*Settings).CommitGroup`、`(*Settings).CommitGlobal` 和 `(*Settings).CommitRegistrations`。

Schema 版本 2 保存稀疏的群组和全局覆盖值、群组和全局 revision、旧格式兼容镜像、owner 认领 nonce 及过期时间、owner ID、持久控制群组、已注册群组、注册 capability 和待注册记录。有效值按运行时覆盖、不可变 `config.json` baseline、内置默认值的优先级重建。

群组和全局提交采用乐观 revision。没有设置路径时，提交更新内存快照并报告未持久化成功。注册提交必须具有真实持久路径。写入成功后才发布不可变快照，因此读取者不会看到写入失败的覆盖值。

- 文件缺失：从 baseline 开始，首次持久提交可创建文件。
- JSON 损坏：原文件成功改名为 `.corrupt` 后，进程从 baseline 开始且仍可写；后续提交会创建新的 schema 版本 2 状态。旧 owner 和注册信息在人工恢复前不可用。备份重命名失败时，程序停用设置写入，并把原文件留在原路径。
- 现有文件不可读：从 baseline 开始，但把设置标记为不可用；群组、全局和注册写入在重启前全部失败，原文件不会覆盖。
- 版本更新于当前支持版本或版本非法：保留文件并停用写入。
- 路径不可写：每次提交失败，原有效快照继续使用。

版本 0 和 1 有明确迁移。适用迁移还会导入旧 `antispam.json`；当前版本 2 不读取该旧文件。

## 升级与回滚

当前版本首次将版本 0 的 `settings.json` 写成 schema 版本 2 前，会按原子写入流程尽力把原文件逐字节备份为 `settings.json.v0.bak`。备份失败会记录 `ERROR`，但不会阻止迁移。该文件只保存升级前的版本 0 状态，不含升级后的 schema 版本 2 状态，因此不能替代回滚前的备份。

回滚到不支持 schema 版本 2 的旧版本后，旧程序下一次写入设置就会破坏当前状态。旧格式只保留 `enabled`、`name_spoiler` 和 `verify_mode`，会删除全部群组和全局覆盖值及 revision、`registration_revision`、owner 身份和认领信息、已注册群组、enrollment nonce 及待注册记录。再次升级时，程序会把该结果视为版本 0，只能恢复这三个旧设置。旧版本还会读取迁移后不再更新的 `antispam.json`，静默恢复迁移前的 antispam 开关和频道白名单。

回滚前先停止服务并备份整个 `STATE_DIRECTORY`；至少应复制当前的 `settings.json` 和 `antispam.json`。再次启动当前版本前，先还原 schema 版本 2 的 `settings.json`。当前版本不会持续把 antispam 开关和频道白名单镜像到 `antispam.json`；该迁移按设计只从 `antispam.json` 写入 `settings.json`。

## `pending.json`

**实现位置：**`internal/verify` 包；`internal/verify/state.go` 中的 `(*Service).save` 和 `(*Service).load`；`internal/verify/service.go` 中的 `(*Service).Shutdown`。

该文件是有效待验证记录数组，包含群组和用户 ID、群内提示消息 ID、模式和语言、备用答案、已发送提示和一次性标记、已用次数、题目和选项及正确索引、nonce、申请者姓名和截止时间。优雅停止先停止计时器，再执行最终保存。重启时恢复计时器，同时应用故障恢复、群组有效性、容量和题目校验。

文件缺失或损坏后成功备份时，不恢复待验证记录，后续写入仍启用。文件不可读或损坏文件备份失败时，不恢复记录，并清空服务中的路径，避免本进程覆盖可恢复原件。后续保存失败时，存储层记录日志，当前申请仍在内存中处理，但重启后可能丢失。

没有模式字段的旧记录按选择题处理。截止时间调整和重新通知见[故障与恢复](outage-recovery.md)。

## `verifyfail.json`、`agents.json` 和 `heartbeat.json`

**实现位置：**`internal/verify` 包；`internal/verify/state.go` 中的 `(*Service).loadVerifyFails`、`(*Service).saveVerifyFails`、`(*Service).loadAgents`、`(*Service).recordAgent`、`(*Service).loadHeartbeat` 和 `(*Service).saveHeartbeat`。

- `verifyfail.json` 保存群组、用户、失败次数和最后失败 Unix 时间，用于跨重启保持冷却和六小时自动封禁累积窗口。验证成功或自动封禁成功会删除对应记录。
- `agents.json` 保存 AI 诱捕总次数和按自报模型统计的次数。不同模型 key 最多 200 个，超出后归入 `other`。
- `heartbeat.json` 保存最后一次成功连接 Telegram 的 Unix 时间，用于重启时估算故障时长。

每个文件缺失或损坏后成功备份时，均从空值开始。文件不可读或损坏文件备份失败时，只停用本进程对该路径的后续写入。写入失败时，当前内存中的失败记录、统计或 heartbeat 仍有效，调用者不再处理错误。Heartbeat 不可用会失去长故障依据，但不阻止恢复 `pending.json`。

## `warns.json`

**实现位置：**`internal/moderate` 包；`internal/moderate/state.go` 中的 `(*warningState).load`、`(*warningState).save`、`(*warningState).increment` 和 `(*warningState).clear`。

该文件保存正数 `{group_id,user_id,count}` 记录。内存最多保留 4,096 个计数；超出时优先淘汰次数最少的记录，次数相同时按群组和用户 ID 确定顺序。每次有效警告后保存一次，达到阈值并清除计数后再保存一次。

文件缺失或损坏时从空状态开始。不可读时清空警告路径，保留原文件并停用本进程后续写入。保存错误不会回滚内存增减，也不阻止 Telegram 管理操作。清除后的保存失败可能让旧计数继续留在磁盘，重启后会恢复已经在内存中清除的警告。

## `feed-<chat_id>.json`

**实现位置：**`internal/feed` 包；`internal/feed/feed.go` 中的 `feedStatePath`、`loadFeedState`、`saveFeedState` 和 `(*Service).Run`。

每个目标文件保存 Bug ID 游标、新闻 URL 游标，以及已跟踪 Telegram Bug 消息的显示状态、miss、确定性编辑失败和确认回复重试计数。每次到期轮询后及 feed 停止时保存。旧格式中的 `status` 会迁移为 `state: "<status>|"`。

文件缺失或损坏后成功备份时从空状态开始，下一次成功请求只建立基线，不补发历史。文件不可读或损坏文件备份失败时会设置 `writeDisabled`，后续轮询周期和停止阶段都跳过该路径的保存。保存失败不阻止投递，也不回滚内存游标。重启后只能恢复最后持久成功的游标和跟踪状态。

## 旧文件和生成文件

**实现位置：**`internal/store` 包；`internal/store/settings.go` 中的 `loadLegacyAntispam` 和 `(*Settings).migrateLegacyAntispam`；`internal/store/json.go` 中的 `Load` 和 `ReclaimTemps`。

`antispam.json` 只用于迁移。`settings.json` 不存在或为版本 0、1 时，其全局启用值和白名单会复制到每个群组的 `settings.json` 覆盖值。生产代码不会写 `antispam.json`，版本 2 设置会跳过读取。迁移适用时，旧文件损坏或不可读会停用设置写入。迁移源文件不会删除。

备份重命名成功时，`<name>.corrupt` 保存解码失败的输入，程序不会自动读取。重命名失败时，原文件留在 `<name>`，本进程停用该路径的写入。`.<name>.tmp-*` 是中断的原子写入，匹配存储模式时会在启动阶段清理。

## 不跨重启保存的状态

**实现位置：**`internal/verify` 包；`internal/verify/state.go` 中的 `(*Service).Stats`；`internal/panel` 包；`internal/panel/session.go` 中的 `(*Panel).newSettingsSession`；`main` 包；`cmd/gentoo-zh-verify-bot/main.go` 中的 `main`。

当日批准和拒绝计数、设置面板会话和草稿、管理员肯定结果缓存、DM 和查询限频窗口、查询和软件包及新闻缓存、正在等待的清理计时器，以及暂时告警限频都只存在于内存。AI 诱捕累计统计不属于当日计数，通过 `agents.json` 跨重启保存。

未设置 `STATE_DIRECTORY` 时，本文全部状态文件都不存在，普通运行时状态只保留在内存。Owner 认领、注册 capability 和运行时群组注册会直接失败，不会伪装成已持久化。
