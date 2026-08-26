# 流程参考

请按运维或代码问题选择文档，不要先按 package 查找。每份流程文档都包含失败分支，以及准确的 package 和函数入口。英文索引位于 [`../README.md`](../README.md)。

| 问题 | 文档 | 主要实现入口 |
| --- | --- | --- |
| 入群申请如何经过题目、通过、冷却或自动封禁？ | [申请者流程](applicant.md) | `internal/verify.(*Service).OnJoinRequest`、`internal/verify.(*Service).OnAnswer`、`internal/verify.(*Service).OnKernelAnswer` |
| 管理员可使用哪些命令和验证按钮？DM 设置面板如何提交变更？ | [管理员流程](admin.md) | `internal/bot.(*Service).handlerRoutes`、`internal/panel.(*Panel).OnSettings`、`internal/panel.(*Panel).OnSettingsCallback` |
| 封禁、清理、禁言、警告和频道身份过滤在权限或 API 调用失败时如何处理？ | [管理操作](moderation.md) | `internal/moderate.(*Service).moderate`、`internal/moderate.(*Service).OnWarn`、`internal/moderate.(*Service).FilterChannelSenders` |
| Feed 如何轮询、去重、编辑、淘汰和处理限频重试？ | [Feed 轮询和投递](feed.md) | `internal/feed.(*Service).Run`、`internal/feed.postFeedItems`、`internal/feed.refreshTracked` |
| 只提供 token 的首次启动如何完成 owner 认领、群组注册和权限检查？ | [部署](deployment.md) | `main.main`、`main.(*registrationService).EnsureOwnerClaim`、`internal/moderate.(*Service).CheckGroupSetup` |
| Telegram 或网络故障及重启期间，验证超时为什么不会误伤申请者？ | [故障与恢复](outage-recovery.md) | `internal/verify.(*Service).RunHeartbeat`、`internal/verify.(*Service).onExpiry`、`internal/verify.(*Service).onRecovery` |
| 每个状态文件保存什么？哪些状态跨重启？文件不可读、损坏或不可写时如何处理？ | [状态与持久化](state-persistence.md) | `internal/store.Load`、`internal/store.Write`、`internal/store.(*Settings).CommitGroup` |
| `config.json` 里能放什么、哪些改成了设置面板、默认值是多少？ | [配置参考](configuration.md) | `internal/config.LoadConfig`、`internal/store.LoadBaseline` |
| 变更需要通过哪些构建、race、CI、发布、版本、本地化和状态 fixture 检查？ | [开发、CI 和发布](development.md) | `main.main`、`internal/i18n.TestProductionCodeContainsNoChineseStringLiterals`、状态兼容生成测试 |
