# 开发、CI 和发布

仓库要求 Go 1.26.7，使用 telego v1.11.2。生产代码位于 `cmd/gentoo-zh-verify-bot` 和 `internal/*`，不是根目录中的单一 package。

## 构建和本地运行

**实现位置：**`main` 包；`cmd/gentoo-zh-verify-bot/main.go` 中的 `main`；模块定义 `go.mod`。

构建全部 package：

```sh
go build ./...
```

构建用于部署的静态命令：

```sh
CGO_ENABLED=0 go build -trimpath -o gentoo-zh-verify-bot ./cmd/gentoo-zh-verify-bot
```

普通构建报告版本 `dev`。执行 `./gentoo-zh-verify-bot -version` 会输出链接时设置的版本，并在检查 `BOT_TOKEN` 前退出。正常运行要求 `BOT_TOKEN`，配置文件可以不存在。运行时初始化见[部署](deployment.md)。

## 测试和本地化约束

**实现位置：**`internal/i18n` 包；`internal/i18n/invariants_test.go` 中的 `TestProductionCodeContainsNoChineseStringLiterals` 和 `TestLocaleFilesLoad`；`main` 包；`cmd/gentoo-zh-verify-bot/main.go` 中的 `main`。

使用 race detector 且禁止测试缓存：

```sh
go test -race -count=1 ./...
```

测试按 package 组织，覆盖行为、持久格式兼容、解析器 fixture、处理器顺序和设置集成。本地化测试也由该命令执行。生产代码在 `internal/i18n` 之外出现中文字符串字面量时，`TestProductionCodeContainsNoChineseStringLiterals` 失败；locale 数据缺失或损坏时，`TestLocaleFilesLoad` 失败。新增用户可见文本必须修改 typed catalog 和全部 locale，不能把文本直接写入处理器。

Race 测试会用 `-race` 执行测试，但不会自动形成真实 Telegram、Bugzilla、GitHub 或 Gentoo 端到端验证。多数服务测试使用伪 transport。

## CI 检查

**实现位置：**工作流 `.github/workflows/ci.yml`；`internal/i18n` 包；`internal/i18n/invariants_test.go` 中由测试步骤覆盖的 `TestProductionCodeContainsNoChineseStringLiterals`。

CI 在 pull request 和向 `main` 推送时按以下顺序执行：

1. `gofmt -l .` 不得输出文件；
2. `go vet ./...`；
3. Staticcheck v0.8.1；
4. `go build ./...`；
5. `go test -race ./...`；
6. Govulncheck v1.7.0；
7. Gosec v2.28.0，只排除已说明的 `G304`、`G703` 和 `G706`，对应运维人员控制的路径及 journald 日志输入。

这些工具通过 `go run` 使用固定模块版本。本地验收应复现 `.github/workflows/ci.yml` 中的命令，不能以某个 package 的测试替代完整检查。

## 持久格式兼容

**实现位置：**`internal/verify` 包；`internal/verify/state_compat_test.go` 中的 `TestStateCompatGenerateFixtures`；`internal/feed` 包；`internal/feed/state_compat_test.go` 中的 `TestStateCompatGenerateFeedFixtures`；`internal/moderate` 包；`internal/moderate/state_test.go` 中的 `TestGenerateWarningFixture`；`internal/store` 下的兼容测试，以及 `testdata/state/` fixture。

`testdata/state/` 是兼容约束。修改任何持久 JSON 格式时，必须明确决定并测试向后兼容行为，再在同一变更中有意识地更新受影响 fixture。不得把重新生成 fixture 当作格式化清理或无关副作用。

当前显式生成器按 package 分开：

```sh
UPDATE_STATE_COMPAT_FIXTURES=1 go test ./internal/verify -run '^TestStateCompatGenerateFixtures$'
UPDATE_STATE_COMPAT_FIXTURES=1 go test ./internal/feed -run '^TestStateCompatGenerateFeedFixtures$'
UPDATE_STATE_COMPAT_FIXTURES=1 go test ./internal/moderate -run '^TestGenerateWarningFixture$'
```

只执行与本次格式变更相关的生成器。代码中没有对应的 `settings.json` fixture 生成器；存储兼容测试要求修改时，应人工更新并审查。必须检查每个 fixture diff，包括字段删除和旧格式文件，再执行完整 race 测试和 CI 检查。历史旧格式 fixture 不能被静默改写为当前 schema。

## 发布和版本注入

**实现位置：**工作流 `.github/workflows/release.yml`；`main` 包；`cmd/gentoo-zh-verify-bot/main.go` 中的 `main` 和 `version` 变量。

推送任意匹配 `v*` 的 tag 会触发发布。触发条件宽于语义版本 `vX.Y.Z`，因此 tag 格式依赖仓库操作策略，工作流本身不校验。

发布 job 先重复完整 CI 检查，再交叉构建 `linux/amd64` 和 `linux/arm64`：

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=<arch> \
  go build -trimpath -ldflags "-s -w -X main.version=<tag>" \
  -o dist/gentoo-zh-verify-bot-linux-<arch> ./cmd/gentoo-zh-verify-bot
```

`-X main.version=<tag>` 替换默认 `dev`，用于 `-version`、启动日志、`/ping` 和发布诊断。工作流生成 `dist/SHA256SUMS`，并通过 GitHub release action 发布两个二进制文件和校验和文件。工作流不构建其他操作系统或架构。
