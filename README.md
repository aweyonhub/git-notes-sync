# git-notes-sync

> 面向 Git 工作区的自动同步工具。以 Markdown / Obsidian 笔记为首要场景，核心能力保持通用。
> 同步模型：`可选提交 → 保护工作区 → fetch → merge（非 rebase）→ 保留文本冲突 → merge commit → push`。

Go 实现，调用系统 Git，不重新实现 Git。详见 [doc/git-notes-sync.md](./doc/git-notes-sync.md)（规格与实现决策）。

**📖 普通 sync 使用说明见 [doc/USAGE.md](./doc/USAGE.md)**（安装、命令、配置、定时调度、AI、冲突处理、FAQ）。

## 安装

### npm（推荐，免编译）

**双源独立分发**：npm registry 走平台分包（主包 `packages/meta/` 无任何 install 脚本，二进制随平台子包 `@aweyonhub/git-notes-sync-<os>-<arch>` 自动安装）；GitHub 直装走仓库根（方案① postinstall 下载器，从 GitHub Releases 下载，自包含不依赖 registry）。提供 `gns`（主命令）/ `notes-sync`（别名）。

```bash
# 方式一：npm registry（子包发布后可用，零 flag）
npm install -g @aweyonhub/git-notes-sync

# 方式二：GitHub 直装（走 main 分支方案①：postinstall 下载器，三 flag 必需）
npm install -g --install-links=true --foreground-scripts --allow-scripts=git-notes-sync github:aweyonhub/git-notes-sync
# 开发版（临时开发分支，如 <branch>）
npm install -g --install-links=true --foreground-scripts --allow-scripts=git-notes-sync github:aweyonhub/git-notes-sync#<branch>
```

> **三个 flag 只适用于方式二**（github 直装拉取的是 main 分支，其 postinstall 下载器从 GitHub Releases 下载二进制）：`--install-links=true` 强制复制解包（npm 对 git 依赖默认符号链接到 cacache 临时目录，被清后包失效）；`--allow-scripts=git-notes-sync` 放行 postinstall（npm 11+ 默认拦截）；`--foreground-scripts` 前台执行脚本避免 reify 竞态。方式一 registry 安装无任何脚本、零 flag。
>
> **两条链路完全独立、互不依赖**：方式一（registry）二进制在子包内，安装零脚本、不访问 GitHub Releases；方式二（github 直装）由仓库根 package.json 的 `postinstall` 执行 `npm/scripts/install.js`（下载器：302 跟随 / SHA-256 校验 / 代理 / 版本覆盖），从 GitHub Releases 下载二进制——**不需要 registry 上的任何包**，子包是否发布不影响本方式。

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
gns config list|get|set|unset  # 查看与编辑配置
gns resolve       # 列出已持久化的冲突 markers
gns resolve --ours | --theirs   # 保留单侧，去 markers，提交并推送
gns resolve --ai                # AI 语义合并（需配置 [ai]）
gns install       # 一键注册定时任务（macOS launchd / Linux systemd·cron / Windows 任务计划）
gns uninstall     # 卸载定时任务
gns logs          # 查看定时任务日志（-n 行数 / -f 跟随 / --path 打印路径）
gns daemon        # 轻量 daemon（Windows 首选，timer 轮询多仓库）
gns version
```

### map：把本机文件纳入 Git 仓库（短命令 `gnm`）

`map` 通过映射把 dotfile、config、skill、脚本等本机文件统一纳入一个专用 git-root 仓库，跨机器同步而不改变文件的日常使用方式（软链接或增量复制）。参见 [使用说明](./doc/USAGE_MAP.md)、[开发状态](./doc/STATUS_MAP.md) 和 [完整设计](./doc/git-notes-sync_map.md)。

```bash
gnm config git-root <path>      # 指向集成仓库；map-root <name> 设置机器命名空间
gnm config add -a <repo> <local>   # 增加映射（-A 为公共区 scope）
gnm init                        # 创建机器 worktree 并应用全部映射
gnm status                      # 状态 + 每个映射事实 + 下一步命令
gnm add <path...> | -A          # 选择本机版本并暂存（get 采用 HEAD 版本）
gnm commit / push / sync        # 提交；人工确认推送（armed .syncable）；自动同步
gnm pull [-f]                   # 冲突/分叉后的恢复入口（不动真实文件）
```

安全模型：首次与阻断后必须人工确认（`.syncable` 闸门），唯一冲突点在 worktree 合并 git-root，网络等异常不伪装成冲突。启用定时：`gns config set map.sync true`，现有调度器每轮额外执行一次 `gns map sync`。

### 定时调度

- **macOS**：`gns install` 一键注册 launchd LaunchAgent（开机自启）：

  ```bash
  gns install            # 默认：按配置 sync_interval 触发一次 gns sync-all（无状态）
  gns install --daemon   # 常驻 daemon 模式（KeepAlive，节奏 = 配置 sync_interval）
  gns install -interval 600    # 改触发间隔；-force 覆盖已有配置
  gns uninstall          # 卸载 agent（不影响二进制 / 配置 / 仓库）
  ```

  详细说明（plist 内容、日志、验证、凭据要求）见 [doc/USAGE.md](./doc/USAGE.md) §3/§5；也可手写 plist。

- **Linux**：`gns install` 一键注册（默认 systemd user units，`--cron` 切 crontab）：

  ```bash
  gns install            # systemd timer：按配置 sync_interval 跑一次 gns sync-all（无状态）
  gns install --daemon   # systemd service 常驻（Restart=always，节奏 = 配置 sync_interval）
  gns install --cron     # 改用 crontab（`*/5 * * * * gns sync-all`；--daemon 时用 @reboot）
  gns uninstall          # 卸载（删除 unit 文件 / crontab 块，不影响二进制与配置）
  ```

  日志走 `journalctl --user -u gns`（cron 模式在 `~/.local/state/git-notes-sync/`）；传统 crontab 手写方式：`*/5 * * * * cd ~/notes && gns sync`（cron 环境需完整：SSH agent、credential helper、PATH/HOME）。
- **Windows**：`gns install` 一键注册任务计划（`schtasks`，无需管理员）：

  ```bash
  gns install            # 任务计划每分钟触发 gns sync-all（最小 1 分钟）
  gns install --daemon   # ONLOGON 启动常驻 daemon
  gns uninstall          # 删除任务
  ```

  日志在 `%LOCALAPPDATA%\git-notes-sync\<label>.log`；查看：`schtasks /Query /TN com.git-notes-sync`。⚠️ 任务计划无 keep-alive，daemon 崩溃不会自动重启。传统方式：`gns daemon`（内置 timer，默认 600s），配合任务计划程序开机自启。

## 配置

**推荐**：所有参数统一放全局配置（macOS：`~/Library/Application Support/git-notes-sync/config.toml`；Linux：`~/.config/git-notes-sync/config.toml`；Windows：`%APPDATA%\git-notes-sync\config.toml`，即 `os.UserConfigDir()` 默认位置），用 `gns repos add` 维护多仓库名单，**无需在每个项目里放配置**；仓库级 `.notes-sync.toml` 仅作为单仓库覆盖（一般没必要）。想自定义位置（如 macOS 下把配置放 `~/.config` 方便 dotfiles 管理）可设环境变量 `GNS_CONFIG`（支持 `~/` 展开，所有命令 + `gns install` 生成的 plist 自动跟随）。完整示例见 [example.config.toml](./example.config.toml)。

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
text_extensions = [".md", ".markdown", ".txt"]

[ai]                          # 可选；任何故障自动降级，不阻塞同步
type = "api"                  # api | command
base_url = "https://api.example.com/v1"
model = "model-name"
api_key_env = "NOTES_AI_API_KEY"
agent_file = "AGENTS.md"      # 仓库级 agent 指令文件，随 diff 发给 AI（默认）
# type = "command"
# command = "codex exec ..."  # stdin = diff（存在 system 指令/AGENTS.md 时
#                             以 "### Instructions" + "### Input" 两段包裹），
#                             stdout = commit message
```

## 行为要点

- **提交时机**：debounce 防打断编辑；`max_wait` 基于 `.git/git-notes-sync.state` 记录"首次发现修改"时间，跨 cron 无状态运行仍可兜底强制提交。
- **提交信息**：`timestamp`/`static` 模式附带 diff 摘要（文件列表 + 行数增减）；`ai` 模式由 AI 生成（`git diff --cached` 截断到 `max_diff_bytes`），失败 fallback 到 `ai_fallback`。
- **冲突不阻塞**：文本冲突保留双方内容与 markers → `git add` → merge commit → push；冲突成为可延迟解决的持久状态，`gns resolve` 事后处理。二进制冲突按 `binary_strategy` 保留本地副本或中止。
- **可靠性**：fetch/push 指数退避重试（`retry_attempts`），认证/权限等确定性错误立即返回不重试；`.git/git-notes-sync.lock` 防并发（10 分钟过期）；merge/rebase 进行中不叠加操作；push 被拒（远端移动）自动重 fetch + 重 merge，最多 3 轮。
- **保护未提交内容**：merge 由 git 原生拒绝覆盖本地修改；此时跳过该仓库并提示。

## 开发

```text
internal/
  config/   配置加载与合并（默认值 ← 全局 ← -c ← 仓库级）
  git/      系统 git 封装
  commit/   提交时机（debounce/max_wait/state）与消息生成
  ai/       OpenAI-compatible API / CLI 双后端，统一降级
  sync/     同步引擎、冲突处理、resolve、status、marker 解析
  mapsync/  map 功能：本机文件映射进 git-root（worktree/link/copy/.syncable）
  daemon/   轻量 timer daemon（配置变更自动重载）
  cli/      命令分发
```

发布：打 tag（如 `v0.1.1`）→ GitHub Actions 测试 + 交叉编译 6 平台 + 生成 `checksums.txt` → Release 资产；npm 侧经平台分包（meta 包 + 6 个 os/cpu 子包）发布到 npm registry（流程见 `doc/STATUS.md` §四）。开发在临时分支进行（如 `mac-launch`），验证后合并 main 发布。
