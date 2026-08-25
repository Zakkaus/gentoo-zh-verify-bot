# Feed 轮询和投递

Feed 服务是可选功能。每个非零目标有独立游标和轮询间隔；条件允许时，同一轮到期目标共享上游请求。

## 启动和轮询时间

**实现位置：**`internal/feed` 包；`internal/feed/feed.go` 中的 `(*Service).Run` 和 `pollAllWithSources`；`internal/config` 包；`internal/config/config.go` 中的 `LoadConfig` 和 `(*FeedConfig).Interval`。

`LoadConfig` 把旧的单项 `feed` 合并到 `feeds`。`chat_id=0` 的目标在启动时停用；后续重复的非零 chat ID 会被忽略，因为状态按 chat 保存。Feed 语言只能为空、`zh`、`zh-Hant` 或 `en`；其他非空值会导致配置加载失败。

`Run` 为每个目标加载状态，执行尽力而为的权限检查，然后立即轮询。进程 ticker 使用全部配置中的最短间隔。只有达到自身 `nextDue` 的目标才会处理；处理后，下一次时间以本轮开始时间加各自间隔计算。间隔未设置或不大于零时为五分钟；1 至 59 秒会调整为 60 秒。

游标相同的到期目标共享一次新 Bug 请求。游标不同则分别请求，避免首次建立基线的目标跳过其他目标的积压。新闻只请求一次。全部到期目标的已跟踪 Bug ID 先去重，再按每组 50 个重新请求。新 Bug 和新闻请求各有 30 秒上下文；每组已跟踪 Bug 也有独立 30 秒上下文。一组失败不阻止后续组。

轮询发生 panic 时，程序恢复并记录日志，之后的 ticker 周期继续运行。来源错误只影响当前周期，不会结束 feed goroutine。

## 游标、首次轮询和顺序

**实现位置：**`internal/feed` 包；`internal/feed/feed.go` 中的 `fetchRecentBugsWith`、`collectRecentBugs` 和 `postFeedItems`。

Bug 游标是已经完整处理的最大 Bug ID。没有游标时，程序查询 Bugzilla 中最新的一条，只记录其 ID，不补发历史。之后查询严格大于游标的 ID，按升序排列，每次最多 100 条。重复 ID 和不大于游标的 ID 会被丢弃。被产品或组件过滤掉的 Bug 也算处理完成，并推进游标。

Bug 按 ID 升序发送。发送成功后推进游标并记录 Telegram 消息 ID。单条 Bug 被分类为永久内容拒绝时，程序记录 Bug ID、推进游标并继续处理下一条。暂时性失败和目标级失败（例如 chat 不可用或缺少发布权限）会停在该 Bug 且不推进，后续轮询重试。每个目标每轮最多处理 100 条，因此更大的积压按连续批次消化。

新闻游标保存最后处理的 URL。首次状态只记录抓取结果中的第一个 URL，不补发历史。保存的 URL 仍在页面中时，程序按从旧到新的顺序发送其前面的新条目。保存的 URL 已不在页面中时，代码无法区分归档窗口过期和来源重排，因此重新以第一个 URL 建立基线，并明确跳过无法确认的条目。

单条新闻被判定为永久拒绝时，游标跳过该条目。其他发送失败会停止且不推进。代码假定 Gentoo 新闻解析结果的第零项最新；仓库代码没有证明上游顺序。

## 发布和原消息编辑

**实现位置：**`internal/feed` 包；`internal/feed/feed.go` 中的 `postFeed`、`refreshTracked` 和 `confirmNotice`。

每次 Telegram 发送或编辑使用 15 秒子上下文。新建且状态为 `UNCONFIRMED` 的 Bug 静默发送；其他未解决 Bug 会通知，除非设置 `silent_bugs`。首次发现时已经有 resolution 的 Bug 静默发送；只有 `FIXED` 使用成功标记，其他关闭结果使用未修复标记。

成功发送且消息 ID 非零的 Bug 都会跟踪。`status|resolution` 任一部分变化时，程序编辑原消息，包括确认、解决、重新打开和修改 resolution。保存状态从 `UNCONFIRMED` 变为另一种未解决且应通知的状态时，还会发送一条非静默回复。直接变为已解决时只编辑原消息。单条回复被永久拒绝时，程序立即放弃回复并推进状态。其他回复发送失败时，旧状态保持不变，以便后续周期重新编辑并发送；连续失败十次后放弃回复并推进状态。

Telegram 返回消息未修改时按成功处理。已知永久不可编辑的消息会立即移出跟踪。其他确定性的 400 编辑错误连续十次后移出。传输、超时、取消和 5xx 错误保留跟踪，并把确定性失败计数清零。编辑成功后更新状态；已解决 Bug 继续保留，以便重新打开后再次编辑。

## 跟踪上限和淘汰

**实现位置：**`internal/feed` 包；`internal/feed/feed.go` 中的 `(*feedState).trackBug`、`(*feedState).evictOne` 和 `refreshTracked`。

每个目标最多跟踪 200 个 Bug。已满时插入新记录，优先淘汰 ID 最小的已解决 Bug；没有已解决记录时，才淘汰 ID 最小的未解决 Bug。遇到非法 key 或空记录时直接移除。

一次完整重新请求未返回某个已跟踪 ID 时，其 miss 计数加一。连续十次完整请求缺失后淘汰。只要任一请求分组失败，本轮缺失 ID 就不增加 miss；成功分组返回的记录仍可编辑。

每个目标每轮最多尝试 20 次编辑。跟踪 map 使用 Go map 遍历顺序，因此代码没有定义本轮具体选择的记录和顺序。未处理的变化保留旧状态，留给后续周期。

## 限频、节奏和重试

**实现位置：**`internal/feed` 包；`internal/feed/feed.go` 中的 `postFeed`、`postFeedItems` 和 `refreshTracked`；`internal/tg` 包；`internal/tg/errors.go` 中的 `IsRateLimited` 和 `Pace`。

发送成功后以及未遇到限频的编辑尝试后，程序固定等待一秒。已跟踪消息编辑收到 Telegram 429 时，当前目标本轮不再编辑，状态不推进。确认回复遇到 429 时也停止当前刷新。普通 Bug 或新闻发送遇到 429 时只停止当前条目循环；同一目标本轮的后续阶段仍可能执行。

Feed 层没有指数退避，也不使用 Telegram 返回的 `retry_after`。保留的工作通常在该目标下一个配置间隔重试。仓库代码无法确认 telego 内部是否还有重试。

请求、解析和暂时性 Telegram 失败不会推进对应游标或跟踪状态。Bug 和新闻发布都会跳过被分类为单条内容永久拒绝的项目。目标级失败仍可重试，不会推进游标。

## 持久化和停止

**实现位置：**`internal/feed` 包；`internal/feed/feed.go` 中的 `loadFeedState`、`saveFeedState` 和 `(*Service).Run`；`internal/store` 包；`internal/store/json.go` 中的 `Load` 和 `Write`。

每个目标使用 `feed-<chat_id>.json`，保存 `last_bug_id`、`last_news_url`，以及跟踪消息 ID、显示状态、miss 计数、确定性编辑失败计数和确认回复重试计数。旧格式中只有 `status` 的记录会在内存中迁移为 `status|`，并在下次成功写入时规范化。

每个到期目标完成发布和编辑后保存一次；取消运行时再保存一次。主进程最多等待 feed 停止五秒。保存错误由存储层记录，但 feed 忽略返回值；投递和内存游标继续推进。不可写期间重启后，只能恢复文件中的旧游标，并按该状态重试或重新建立基线。

因此，Feed 投递存在一个 at-least-once 窗口。发送成功后只更新内存游标；直到该目标完成整个周期，`saveFeedState` 才写入状态。进程在发送成功后、保存前崩溃时，持久化游标不会变化。重启后会重发已经成功投递的连续前缀：最多包含单轮上限的 100 条 Bug，以及该轮保存前已发送的全部新闻。该取舍允许重复投递，避免静默跳过项目。

文件缺失时从空状态开始。JSON 损坏时尽量改名为 `.corrupt`，再从空状态开始。与验证核心状态不同，feed 读取到不可读文件后不会禁止后续写入，而是每轮继续调用 `Write`。之后能否写入取决于底层路径。所有写入均使用同目录临时文件、文件 `fsync`、原子重命名和父目录 `fsync`。
