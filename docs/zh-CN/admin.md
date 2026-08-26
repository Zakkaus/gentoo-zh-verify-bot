# 管理员流程

本文说明群组命令、验证提示上的管理员按钮，以及 DM 设置面板。封禁、禁言等操作见[管理操作](moderation.md)。

## 路由和授权

**实现位置：**`internal/bot` 包；`internal/bot/bot.go` 中的 `(*Service).handlerRoutes`；`internal/panel` 包；`internal/panel/panel.go` 中的 `(*Panel).runSettingsAdminCmd`。

处理器采用首个匹配路由。验证回调和设置面板输入先于普通命令及 DM 处理。群组设置命令只接受受保护群组，尽力删除命令消息，并实时查询调用者的 Telegram 成员状态。查询失败时按无管理员权限处理，不执行设置变更。

`/ping`、`/stats` 和 `/help` 不要求管理员权限。群内命令和回复采用尽力清理。`/help` 只有在管理员缓存返回肯定结果后才附加管理员命令；查询失败只会省略该段。DM 中的状态以当前控制群组为准。

持久状态中的机器人所有者 ID 为零时，命令目录不会注册所有者私聊范围。所有者认领成功后，程序立即刷新 Telegram 命令菜单，无需重启。所有者私聊的 `BotCommandScopeChat` 菜单包含全部成员命令，以及 `/enroll` 和 `/unregister`。

## 群组设置命令

**实现位置：**`internal/panel` 包；`internal/panel/panel.go` 中的 `(*Panel).OnStart`、`(*Panel).OnStop`、`(*Panel).OnRich`、`(*Panel).OnSpoiler`、`(*Panel).OnVMode` 和 `(*Panel).OnAutoDel`；`internal/moderate` 包；`internal/moderate/service.go` 和 `internal/moderate/antispam.go` 中的 `(*Service).OnBanTime` 和 `(*Service).OnBC`。

- `/start` 和 `/stop` 启用或停用当前群组的入群验证。DM 中的 `/start` 改为打开设置深链接或发送申请者题目。
- `/vmode` 显示或设置 `kernel`、`quiz`、`mixed`；`auto`、`config`、`default` 删除运行时覆盖值。
- `/spoiler` 切换群内验证提示中的申请者姓名隐藏。
- `/autodel` 显示、停用、启用或设置当前群组的查询结果清理时间，范围为 1 至 1,440 分钟。
- `/bantime` 显示或设置当前群组的封禁时长。可用零或永久值；最终值按 Telegram 范围调整。
- `/bc` 切换当前群组的频道身份过滤，或修改白名单。具体行为见[管理操作](moderation.md)。
- `/rich` 是命令入口中的全局设置。

所有变更都经过设置存储。未配置状态目录时，普通群组和全局提交仍会在内存中生效，但不能持久化。校验或写入失败时，原有有效快照保持不变，群内会显示保存失败。

## 验证提示按钮

**实现位置：**`internal/verify` 包；`internal/verify/service.go` 中的 `(*Service).OnAdminAction`、`(*Service).executeApprove` 和 `(*Service).executeBan`。

群内验证提示提供直接批准和拒绝并封禁按钮。每次点击都会实时查询操作者的管理员状态。非管理员或查询失败者会收到仅限管理员的回调结果。目标用户 ID 来自回调数据，不需要回复目标消息。

批准操作先占用待验证记录，再调用 Telegram，因此超时不能并发处理同一记录。批准成功后，程序删除待验证记录和旧失败次数，并尽力删除已记录的群内验证消息和私聊验证消息。批准失败时重新开放记录、启动重试计时，并通知运维人员。

封禁操作先占用待验证记录，按群组当前封禁时长执行带消息撤回的封禁，再请求 Telegram 拒绝入群申请。程序在这些网络操作前先确认回调。两项操作都成功后，程序才会删除待验证记录及已记录的两条验证消息。任一操作失败时，程序都会重新开放同一条记录，保留两条验证消息，提供不计失败次数的重试窗口，并发送运维告警和本地化群内结果。

## 控制群组策略

**实现位置：**`internal/config` 包；`internal/config/config.go` 中的 `(*Config).ControlGroupAllowed`；`internal/store` 包；`internal/store/settings.go` 中的 `(*Settings).ControlGroupID`；`internal/panel` 包；`internal/panel/panel.go` 和 `internal/panel/settings_input.go` 中的 `(*Panel).runSettingsAdminCmd` 和 `(*Panel).applyTextInput`。

当前代码存在两条不同的策略路径：

- 命令入口中的全局设置 `/rich` 调用 `Config.ControlGroupAllowed`。`control_group_id` 非零时只允许该群组；为零时允许所有受保护群组。
- 设置面板中的全局 DM 查询频率使用 `Settings.ControlGroupID`。该方法优先返回持久注册信息中的控制群组，否则返回第一个有效群组。其他群组可以查看该值，但不能修改。

原本没有有效群组的部署注册第一个运行时群组时，会写入持久控制群组，但启动时生成的有效配置快照不会随注册更新。因此，未配置 `control_group_id` 的部署没有统一规则：所有受保护群组的管理员都能执行 `/rich`，只有设置存储认定的控制群组能在面板中修改全局 DM 查询频率。本文只记录现有行为，不代表设计意图。

## 打开设置面板并选择群组

**实现位置：**`internal/panel` 包；`internal/panel/settings_panel.go` 中的 `(*Panel).OnSettings`、`(*Panel).openSettingsStart` 和 `(*Panel).eligibleGroups`；`internal/panel/session.go` 中的 `(*Panel).newSettingsSession`。

`/settings` 必须由当前管理员在受保护群组中执行。机器人解析自己的用户名，创建与用户绑定的会话，并在群内回复一个 DM 深链接。授权查询失败、调用者不是管理员、机器人用户名或 `GetMe` 不可用、会话达到上限、token 生成失败或启动消息发送失败时，面板不会打开。

会话有效期为 30 分钟，最多同时存在 256 个；每个用户只能有一个。渲染新页面会轮换 token，但不会延长有效期。新会话会作废该用户的旧会话；进程重启会作废全部会话和未保存草稿。在 DM 中打开链接时，程序检查 token、所有者 ID，以及用户在入口群组中的实时管理员状态。错误或过期 token 会被拒绝；权限已经丢失时，会话被删除。

群组选择器只列出机器人仍在其中、且该用户为管理员的有效群组。入口群组复用刚完成的授权；其他群组均实时查询。查询失败的群组不会显示。选择群组时再次确认机器人成员状态，记录群组和全局 revision，再打开群组主页。每页最多八个群组。

## DM 中可修改的设置

**实现位置：**`internal/panel` 包；`internal/panel/settings_panel.go` 中的 `(*Panel).dispatchRuntime`、`(*Panel).dispatchList`、`(*Panel).dispatchModeration` 和 `(*Panel).dispatchVerificationParameters`；`internal/panel/settings_input.go` 中的 `(*Panel).dispatchChannel`。

面板显示每个值来自运行时覆盖、`config.json` 还是内置默认值，并可修改：

- 运行参数：验证开关、验证题发送方式（`group`、`dm` 或默认的 `both`）、模式、姓名隐藏、封禁时长、查询自动删除及 TTL、群组语言；
- 列表：频道身份白名单、受信任成员群组、已知或支持群组；
- 验证参数：30 至 1,800 秒超时、最大失败次数或关闭、冷却时间或关闭、被他人邀请的成员是否仍需验证，以及全局 DM 查询频率；
- 管理设置：频道身份发言拦截、`/mute` 时长、`/warn` 上限，以及两项全局开关——富文本输出和告警聊天；
- 必加频道：选择频道、设置或清除私有邀请、停用频道检查。

管理设置里的禁言时长必须能自行解除，因此不接受 0（永久）；单次 `/mute <时长>` 仍可覆盖该默认值。告警聊天用 Telegram 群组选择器指定，决定运维告警发往哪里；清除后恢复原有行为，即发到出问题的那个群。这两项全局开关只能从控制群修改，改完立即生效，无需重启。

`delivery_mode` 的内置基线为 `both`，也可以在 `config.json` 中设置全局值或按群值。面板中的三个按钮按照当前 revision 提交按群稀疏覆盖值。选择与基线相同的值时，程序删除该覆盖值。其他提交同时修改该群组时，面板按相邻运行参数控件的相同规则结束会话并报告冲突。

添加列表项使用 Telegram 群组选择器。提交者必须仍是所选群组或频道的成员。选择必加频道时，机器人也必须仍在其中。没有用户名的私有频道必须先提供有效的 `https://t.me/...` 邀请，之后频道 ID、显示名和邀请才会一并提交。重复添加不写入。删除不存在的项目按并发修改处理。加入频道身份白名单时先提交设置，再尝试解除频道身份封禁；解除失败只提示部分失败，不回滚白名单。

同一用户存在内核验证时，面板不允许开始文本或群组选择输入，避免设置回复被当作内核答案。输入开始后出现内核验证时，面板取消该输入。程序只接受精确匹配的 ForceReply 消息 ID 或群组选择请求 ID。数字、时长、URL、群组或空文本无效时，在代码支持可恢复校验的分支中保留输入，供用户修正。

## 题库

**实现位置：**`internal/panel` 包；`internal/panel/settings_input.go` 中的 `(*Panel).dispatchQuizDraft`、`(*Panel).dispatchFallbackDraft` 和 `(*Panel).dispatchConfirmation`。

选择题库支持分页、新增、编辑、删除、添加或删除选项，以及指定正确选项。保存要求题目非空、至少两个选项，并且正确选项有效。允许空选择题库；无法生成选择题时，验证模式会回退到内核题。

无 Linux 备用题库支持新增、编辑、删除、添加或删除答案，以及恢复本地化内置题。自定义备用题必须有非空题目和至少一个答案。删除最后一道自定义备用题会自动恢复内置题。删除题目、停用必加频道和恢复内置题都要再次确认。取消操作不改变持久数据。

草稿只存在于面板会话，点击保存后才提交。会话过期、关闭、取消或 revision 冲突都会丢弃未保存草稿。

## Revision、旧控件和失败处理

**实现位置：**`internal/panel` 包；`internal/panel/settings_panel.go` 和 `internal/panel/settings_input.go` 中的 `(*Panel).OnSettingsCallback`、`(*Panel).OnPanelInput` 和 `(*Panel).OnPanelChatShared`；`internal/store` 包；`internal/store/settings.go` 中的 `(*Settings).CommitGroup` 和 `(*Settings).CommitGlobal`。

每次渲染面板都会轮换回调 token，并绑定所有者、当前页面、选中群组、DM 会话和面板消息 ID。旧按钮、非法回调、复制的回调或其他用户的点击都不能修改设置。每个回调和输入提交都会重新检查管理员权限。

导航、翻页、刷新和取消可以接受更新后的群组 revision。设置变更、草稿操作、确认或输入提交必须匹配会话或提示记录的 revision。发生冲突时，面板结束会话并提示并发修改，不执行合并。全局 DM 查询频率还要单独检查全局 revision。

`CommitGroup` 和 `CommitGlobal` 先构建并校验候选值；配置持久化时，原子写入成功后才发布新快照。写入或校验失败会结束当前面板会话并显示保存失败。Telegram 发送、编辑或删除失败可能留下旧消息或键盘，但不能绕过 revision 和授权检查。
