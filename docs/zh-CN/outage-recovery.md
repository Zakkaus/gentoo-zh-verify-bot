# 故障与恢复

恢复机制避免机器人无法连接 Telegram 时耗尽申请者的答题时间。程序不会全局停止计时器；每次到期处理都会先检查连接状态。

## Heartbeat 和故障判定

**实现位置：**`internal/verify` 包；`internal/verify/state.go` 中的 `(*Service).RunHeartbeat`、`(*Service).heartbeatTick` 和 `(*Service).offlineNow`；`main` 包；`cmd/gentoo-zh-verify-bot/main.go` 中的 `main`。

进程启动一个 heartbeat goroutine。首次探测在 25 秒后执行，之后每 25 秒执行一次。每次 `GetMe` 使用十秒上下文。成功后更新 `lastOnline`，并尽力写入 `heartbeat.json`。失败时记录验证超时已暂停，但不改变 `lastOnline`。

最近一次成功连接超过 70 秒后，服务视为离线。两次成功探测间隔超过 90 秒时，下一次成功会触发恢复。两个阈值用途不同：到期处理还会实时探测，因此可在 heartbeat 超过 70 秒之前发现连接故障。

Heartbeat 写入失败不停止探测，除共享存储日志外没有额外反馈。上下文取消后，循环直接结束。

## Telegram 不可达时到期

**实现位置：**`internal/verify` 包；`internal/verify/state.go` 中的 `(*Service).onExpiry`、`(*Service).reachable` 和 `(*Service).deferExpiry`。

待验证计时器触发后，`onExpiry` 先检查 heartbeat 是否过旧，并在需要时执行一次十秒 `GetMe` 实时探测。任一检查表明 Telegram 不可达时，程序不会消费记录、拒绝申请或增加失败次数。`deferExpiry` 为该记录提供新的完整群组时限，并用新 epoch 启动计时器。

因此，暂停表示不断重新提供完整窗口，不是停止计时器。原到期原因保持不变。普通超时经过故障延期后，如果后续计时器在 Telegram 可达时触发，且持续故障恢复尚未替换原因，仍会按普通超时增加失败次数。

每个计时器捕获待验证 nonce 和递增 epoch。来自被替换申请、旧截止时间或恢复前的计时器都不能处理当前记录。到期时实时探测失败就足以延期，无需运维操作。

## 运行时恢复和重新通知

**实现位置：**`internal/verify` 包；`internal/verify/state.go` 中的 `(*Service).onRecovery` 和 `(*Service).renotifyPending`。

超过 90 秒没有成功连接后，下一次 heartbeat 成功会为每条有效待验证记录提供新的完整截止时间，原因记为不增加失败次数的 `recovered`。程序在锁内取得通知快照，释放锁后再调用 Telegram。

每次恢复最多向 30 名申请者发送 DM 故障通知和新的群内验证提示。超过上限的记录只刷新截止时间。某条记录在自身超时时间内已经重新通知过时，连接反复断开不会再次发送，但仍会刷新截止时间。优雅停止开始后不执行恢复。

DM 发送错误会被忽略。新群内提示发送失败时，程序记录并告警，仍会尽力删除旧提示，并把有效记录的消息 ID 设为零。之后该记录到期时，因为没有确认送达的新提示，所以不会增加失败次数。删除和告警失败不会撤销新窗口。

重新通知结束后，程序保存待验证状态，使新截止时间和群内消息 ID 能跨下一次崩溃恢复。保存失败时，新状态只保留在内存中。

## 重启恢复

**实现位置：**`internal/verify` 包；`internal/verify/state.go` 中的 `(*Service).load` 和 `(*Service).loadHeartbeat`；`internal/verify/service.go` 中的 `New`。

服务构造时加载 `pending.json`，并用 `heartbeat.json` 中最后一次成功连接时间估算停机时长。只恢复启动时有效配置中的群组；非法或无法完成的选择题记录会跳过。内核题次数、备用题状态、一次性标记、nonce、语言、题目、截止时间和群内消息 ID 均保留。

估算停机超过 90 秒时，每条恢复记录获得新的完整窗口，且不增加失败次数。最多重新通知 30 条，并保存调整后的状态。停机不足 90 秒时，未来截止时间保留剩余时长；停机期间已经过期的记录获得 60 秒、原因是 `restart-lapsed` 的免失败窗口。

`load` 不会立即保存短重启产生的 60 秒调整。如果另一次崩溃发生在后续待验证状态保存之前，下次重启可能根据旧截止时间再次计算宽限。Heartbeat 文件缺失、损坏或不可读时，没有停机时长依据，因此不会选择长故障恢复。待验证记录仍按保存的截止时间或短重启规则处理。

`pending.json` 不可读时，原文件保持不变，本进程停用待验证状态写入，并且不恢复记录。JSON 损坏时尽量改名为 `.corrupt`，不恢复记录，但后续仍可写入新文件。

## 停止和进程故障

**实现位置：**`internal/verify` 包；`internal/verify/service.go` 中的 `(*Service).Shutdown`；`main` 包；`cmd/gentoo-zh-verify-bot/main.go` 中的 `streamEndedUnexpectedly` 和 `main`。

通过信号停止时，处理器先停止，`Shutdown` 再把验证服务标记为正在停止，停止所有计时器，拒绝之后的结束处理，并保存待验证、失败计数和 heartbeat。与停止并发的计时器不能在冻结后拒绝、计失败或封禁申请者。

Long polling 对暂时错误使用五秒重试。进程上下文仍有效时更新流却结束，程序以非零状态退出，供 systemd 重启。致命路径不会执行优雅停止中的 defer 清理，只能依赖每次事件的原子保存和此前持久化的 heartbeat；尚未完成保存的变更可能不会出现在重启后的状态中。
