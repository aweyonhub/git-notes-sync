# git-notes-sync

> 面向 Git 工作区的自动同步工具。以 Markdown / Obsidian 笔记为首要场景，核心能力保持通用。
> 同步模型：`可选提交 → 保护工作区 → fetch → merge（非 rebase）→ 保留文本冲突 → merge commit → push`。

Go 实现，调用系统 Git，不重新实现 Git。详见 [doc/git-notes-sync.md](./doc/git-notes-sync.md)（规格与实现决策）。

**📖 完整使用说明见 [doc/USAGE.md](./doc/USAGE.md)**（安装、命令、配置、定时调度、AI、冲突处理、FAQ）。

## 安装

### npm（推荐，免编译）

postinstall 按平台从 GitHub Releases 下载对应二进制（`gns-<platform>-<arch>[.exe]`，含 SHA-256 校验），提供 `gns`（主命令）/ `notes-sync`（别名）。

```bash
# 正式版（main 分支 = 最新 Release v0.1.0）
npm install -g --install-links=true --foreground-scripts --allow-scripts=git-notes-sync github:aweyonhub/git-notes-sync
# 开发版（dev 分支，下载对应平台的 bin）
npm install -g --install-links=true --foreground-scripts --allow-scripts=git-notes-sync github:aweyonhub/git-notes-sync#dev
```

> **三个 flag 缺一不可**：`--install-links=true` 强制复制解包（npm 对 git 依赖默认是符号链接到 cacache 临时目录，临时目录被清后包就失效）；`--foreground-scripts` 前台跑 postinstall（避免后台竞态）；`--allow-scripts=git-notes-sync` 放行 postinstall（npm 11+ 默认拦截 install 脚本；npm 12 需 `npm install-scripts approve` + `npm rebuild -g git-notes-sync`）。

### 手动构建

```bash
make build          # 本机二进制 ./gns（版本/commit 经 ldflags 注入）
make cross          # 交叉编译全部平台到 dist/
make test           # 集成测试（需要系统 git）
```

## 使用

```bash
gns sync          # 核心命令：commit → fetch → merge → push
gns sync-all      # 同步配置中所有 repos
gns commit        # 立即提交当前修改（忽略 debounce）
gns commit-ai     # AI 生成 message 后提交
gns status        # 工作区 / 远端 / 冲突状态
gns repos list|add|del   # 维护多仓库列表
gns resolve       # 列出已持久化的冲突 markers
gns resolve --ours | --theirs   # 保留单侧，去 markers，提交并推送
gns resolve --ai                # AI 语义合并（需配置 [ai]）
gns daemon        # 轻量 daemon（Windows 首选，timer 轮询多仓库）
gns version
```

### 定时调度

- **Linux / macOS**：cron 无状态触发，建议 `*/5 * * * * cd ~/notes && gns sync`（cron 环境需完整：SSH agent、credential helper、PATH/HOME）。
- **Windows**：`gns daemon`（内置 timer，默认 60s），配合任务计划程序开机自启；daemon 继承启动它的 shell 环境变量。

## 配置

**推荐**：所有参数统一放全局配置 `~/.config/git-notes-sync/config.toml`（或 `%APPDATA%\git-notes-sync\config.toml`），用 `gns repos add` 维护多仓库名单，**无需在每个项目里放配置**；仓库级 `.notes-sync.toml` 仅作为单仓库覆盖（一般没必要）。完整示例见 [example.config.toml](./example.config.toml)。

```toml
auto_commit = true            # 是否自动提交工作区修改
commit_debounce = 60          # 最近修改距今不足 N 秒则推迟提交
commit_max_wait = 300         # 修改待处理超过 N 秒则强制提交（兜底）
commit_message = "timestamp"  # timestamp | static | ai（均附带 diff 摘要：
                              #   文件列表 + 行数增减；static 首行为固定文本
                              #   commit_static_message，ai 失败降级 ai_fallback）
binary_strategy = "ours"      # 二进制冲突：保留本地副本 | abort

[conflict]
strategy = "preserve"         # 文本冲突保留 markers 并继续同步 | abort
text_extensions = [".md", ".txt", ".yaml", ".yml", ".toml"]

[ai]                          # 可选；任何故障自动降级，不阻塞同步
type = "api"                  # api | command
base_url = "https://api.example.com/v1"
model = "model-name"
api_key_env = "NOTES_AI_API_KEY"
agent_file = "AGENTS.md"      # 仓库级 agent 指令文件，随 diff 发给 AI（默认）
# type = "command"
# command = "codex exec ..."  # stdin = diff，stdout = commit message
```

## 行为要点

- **提交时机**：debounce 防打断编辑；`max_wait` 基于 `.git/git-notes-sync.state` 记录"首次发现修改"时间，跨 cron 无状态运行仍可兜底强制提交。
- **提交信息**：`timestamp`/`static` 模式附带 diff 摘要（文件列表 + 行数增减）；`ai` 模式由 AI 生成（`git diff --cached` 截断到 `max_diff_bytes`），失败 fallback 到 `ai_fallback`。
- **冲突不阻塞**：文本冲突保留双方内容与 markers → `git add` → merge commit → push；冲突成为可延迟解决的持久状态，`gns resolve` 事后处理。二进制冲突按 `binary_strategy` 保留本地副本或中止。
- **可靠性**：fetch/push 指数退避重试（`retry_attempts`）；`.git/git-notes-sync.lock` 防并发（10 分钟过期）；merge/rebase 进行中不叠加操作；push 被拒（远端移动）自动重 fetch + 重 merge，最多 3 轮。
- **保护未提交内容**：merge 由 git 原生拒绝覆盖本地修改；此时跳过该仓库并提示。

## 开发

```text
internal/
  config/   配置加载与合并（默认值 ← 全局 ← -c ← 仓库级）
  git/      系统 git 封装
  commit/   提交时机（debounce/max_wait/state）与消息生成
  ai/       OpenAI-compatible API / CLI 双后端，统一降级
  sync/     同步引擎、冲突处理、resolve、status、marker 解析
  daemon/   轻量 timer daemon（配置变更自动重载）
  cli/      命令分发
```

发布：打 tag（如 `v0.1.0`）→ GitHub Actions 测试 + 交叉编译 5 平台 + 生成 `checksums.txt` → Release → npm 壳 `postinstall` 下载（SHA-256 校验）。开发分支：`dev`。
