# Git Notes Sync

> 面向 Git 工作区的自动同步工具。以 Markdown / Obsidian 笔记为首要场景，核心能力保持通用，可服务于任意文本型 Git 仓库。

---

## 一、项目说明

### 1.1 背景与痛点

AI 正深度介入个人知识库的维护——写笔记、整理内容、生成摘要、批量修改越来越多地由 Agent 完成。在众多数据形态中，**纯 Markdown + Git** 是最适合人机协作的组合：

- Markdown 是大模型最容易生成与解析的格式，全平台通用；
- Git 提供可靠的同步、版本历史与回溯能力。

但日常使用中痛点明显：

- 多端（Mac / Windows）手动 `pull` / `push` 繁琐，容易遗忘或产生冲突；
- 提交时机随意——要么忘记提交，要么每次保存都产生无意义 commit；
- 文本冲突一旦发生就阻塞同步，必须人工介入解决。

本项目的核心命题：**把「同步」自动化、把「提交」智能化、把「冲突」从阻断异常变成可延迟解决的数据状态。**

### 1.2 定位

| 维度 | 说明 |
|------|------|
| 适用场景 | 以 Markdown 为主的笔记 / 文档仓库，可扩展到任意文本仓库 |
| 运行方式 | 独立 CLI + cron 或可选轻量 daemon，不依赖 Obsidian 或其他编辑器常驻 |
| 同步边界 | 默认只同步已 commit 的内容；`auto_commit` 可开关 |
| AI 角色 | 可选增强（生成提交信息、批量解决冲突），非硬依赖 |
| Git 依赖 | 当前调用系统 Git；是否内置纯实现见「六、待定问题」 |
| 插件兼容 | 与 Obsidian Git（含移动版）及其他 Git 同步插件不冲突；本工具在系统层操作 Git，不介入编辑器进程，可共存或互补使用 |

### 1.3 非目标

- **不重新实现 Git**：Git 操作优先调用系统 Git；
- **不做语义级自动合并**：冲突的语义解决交给用户或 Agent 稍后处理；
- **不绑定 Markdown**：Notes 是默认 preset，而非底层限制。
- **不引入重型 daemon**：不做 watcher、状态持久化、suspend / resume 感知等复杂常驻逻辑；可选轻量 daemon 替代 OS 定时调度 + 缓存配置，详见 4.5。

---

## 二、主要目标

1. **自动同步** — 一条命令完成 `fetch → merge → push`，并支持定时任务触发。
2. **智能提交** — `auto_commit` 可配置；提交时机 debounce + max wait 兜底，避免打断编辑也防止长期未落盘；提交信息可自动生成。
3. **冲突不中断同步** — 文本冲突保留 markers 并继续同步，冲突成为「待解决的数据状态」而非同步失败。
4. **AI 增强而非依赖** — AI 生成提交信息、批量解决冲突；任何 AI 故障自动降级，不影响同步链路。
5. **可靠运行** — cron 无状态运行或轻量 daemon 常驻，处理网络抖动与异常状态。

---

## 三、核心设计原则

> Git 负责可靠同步与历史记录；Conflict 可被持久化并延迟解决；Agent 负责语义处理；AI 通过 API 或 CLI 插拔；Notes 是重要场景，但核心能力保持通用。

同步主模型（区别于 `git-auto-sync` 的 `commit → fetch → rebase` + 冲突 abort）：

```text
optional commit
      ↓
保护未提交工作区（不被 pull / merge 覆盖）
      ↓
fetch → merge（非 rebase）
      ↓
可保留的文本冲突 → preserve markers → merge commit
      ↓
push
      ↓
后续由用户 / Agent 语义解决
```

---

## 四、模块设计

按职责拆分为四层，每层解决一类问题，避免把所有需求平铺罗列。

- **核心同步层**：同步引擎、冲突处理——同步如何发生、冲突如何被容忍
- **提交与智能层**：提交管理、AI 集成——何时提交、提交信息怎么写
- **运行时层**：定时调度、可靠性——如何定时触发、如何应对异常
- **交互层**：CLI、技术选型——如何被使用、用什么实现

### 4.1 同步引擎（Sync Engine） · 核心同步层

**职责**：执行同步主流程，保证冲突与故障不阻塞同步。

**关键机制**：

- **保护未提交内容**：无论 `auto_commit` 与否，同步过程不得覆盖用户未提交的修改。
- **merge 而非 rebase**：不采用 `git-auto-sync` 的 `commit → fetch → rebase` 策略，保留双向历史。
- **并发锁**：定时任务可能重叠，使用 lock file 防止同一仓库并发同步。
- **retry**：`fetch` / `push` 网络失败自动重试，临时断网恢复后继续。
- **状态感知**：Git 已处于 merge / rebase 状态时先处理或挂起，不叠加操作。

### 4.2 冲突处理（Conflict Handling） · 核心同步层

**职责**：把「冲突」从同步失败异常，转变为可持久化、可延迟解决的数据状态。

**处理流程**：

```text
text conflict   → 保留双方内容及 markers → 标记 resolved → merge commit → push 并继续
binary conflict → 独立策略（保留本地副本 / 停止并提示）
```

**关键机制**：

- 通用文本冲突策略，不写死 Markdown；通过扩展名或 Git 检测区分 text / binary。
- Markdown / Notes 作为默认 preset，而非底层限制。
- `gns resolve` 处理已持久化的 markers，供用户或 Agent 批量语义解决。

**配置**：

```toml
[conflict]
strategy = "preserve"
text_extensions = [".md", ".markdown", ".txt"]
```

### 4.3 提交管理（Commit Management） · 提交与智能层

**职责**：控制「何时提交」与「提交信息怎么写」，避免每次保存都产生无意义 commit。

**智能提交时机**（借鉴并改进 `git-auto-sync`）：

- **debounce**：定时检查时若最近一次文件修改距今不足 `commit_debounce` 秒，推迟本次提交，等下次定时检查——避免在用户 / Agent 正在编辑时打断。
- **max wait 兜底**：修改等待超过 `commit_max_wait` 秒后强制提交，防止修改长期未落盘。
- **批量提交**：一次提交合并当前所有未提交的修改。

**提交信息内容**：

- `timestamp` / `static` 模式：写入改动文件列表、行数增减等 git diff 摘要，便于追溯。格式示例：

```text
notes: 2026-08-13 11:30

 files: 3 changed, +42, -8
 - docroot/10-note/mac/aerospace.md (+20, -3)
 - docroot/10-note/tools/brew.md (+15, -5)
 - docroot/20-collect/draft.md (+7)
```

- `ai` 模式：由 AI 生成语义化 message（见 4.4），失败自动 fallback 到上述摘要格式。

**提交信息策略**：

```toml
auto_commit = true              # 是否自动提交工作区修改
commit_debounce = 60            # 最近一次修改距今不足 N 秒则推迟提交，等下次定时检查
commit_max_wait = 300           # 最多等待 N 秒，超时后强制提交
commit_message = "timestamp"    # timestamp | static | ai
```

- `timestamp`：时间戳 + git diff 摘要（文件列表、行数增减），如上方示例；
- `static`：固定文本 + git diff 摘要；
- `ai`：AI 生成（见 4.4），失败自动 fallback。

### 4.4 AI 集成（AI Integration） · 提交与智能层

**职责**：为提交信息生成、冲突批量解决提供 AI 能力，保持插拔与可降级。

**API 方式**：直接调用 OpenAI-compatible API，不依赖本地 CLI 安装。Key 默认从环境变量读取。

```toml
[ai]
type = "api"
base_url = "https://api.example.com/v1"
model = "model-name"
api_key_env = "NOTES_AI_API_KEY"
```

流程：`git diff --cached → AI API → commit message → git commit`

**Agent 指令文件**：`[ai] agent_file`（默认 `AGENTS.md`，相对仓库根）指定的指令文件随 diff 一起作为 system prompt 发给 AI（提交信息与冲突解决均生效），不存在则忽略。

**Command 方式**：支持任意 CLI（Codex / OpenCode / Pi / Ollama / 自定义程序）。

```toml
[ai]
type = "command"
command = "codex exec ..."
```

统一约定：`stdin = git diff --cached`，`stdout = commit message`。

**不阻塞同步**：API 不可用、网络错误、quota 用尽、CLI 未安装、返回格式异常等情况，自动 fallback：

```toml
ai_fallback = "timestamp"
```

> AI 只能增强体验，不能成为同步链路的必要条件。

### 4.5 定时调度（Scheduler） · 运行时层

**职责**：定时驱动同步与提交检查，支持两种调度方式。

**方式一：OS 定时任务**（Linux / macOS 首选）

- Linux cron / macOS launchd 定时触发 `gns sync`，工具本身不管理进程生命周期。
- 每次无状态运行，OS 负责调度与恢复。
- 适合 Unix 环境，cron 开箱即用。

**方式二：轻量 daemon**（Windows 首选 / 其他平台可选）

- Windows 缺乏便捷的 cron 替代（Task Scheduler 配置繁琐），轻量 daemon 提供内置定时器。
- **仅做两件事**：内部 timer 周期触发同步、缓存配置避免重复解析。
- **不做的事**：无 watcher、无状态持久化、无 suspend / resume 感知、不管理复杂生命周期。
- 启动方式：手动启动 / 开机自启 / 注册为 Windows 服务；异常退出后重启即恢复，无需状态恢复逻辑。

**两种方式共同行为**：

- 定时 poll 远端：周期 fetch，发现远端新内容即同步。
- 定时提交检查：周期检查工作区修改，结合 debounce 决定是否提交。
- 多仓库：逐个执行 `gns sync`，无需额外管理机制。

> **不引入 filesystem watcher**：本工具面向笔记 / 文档仓库，非大型工程项目。watcher 需处理跨平台差异、递归监听、事件去重、资源占用等复杂问题，对当前场景属于过度设计。定时轮询足够覆盖需求，且实现简单、行为可预期。

### 4.6 可靠性（Reliability） · 运行时层

**职责**：应对定时任务单次运行中可能遇到的异常。

**Git 环境**：使用轻量 daemon 时需注意 `SSH_AUTH_SOCK`、credential helper、`PATH` / `HOME` 环境变量——避免「终端 git push 成功，daemon git push 失败」。cron 方式同样需确保运行环境完整。

| 场景 | 处理 |
|------|------|
| 网络临时断开 | fetch / push 自动 retry |
| Git 处于 merge / rebase | 先感知状态，避免叠加操作 |
| repository 被移动 / 删除 | 检测并跳过 / 提示 |
| 定时任务重叠 | lock file 防止同一仓库并发同步 |

### 4.7 命令行接口（CLI） · 交互层

```bash
gns sync         # 同步当前目录仓库（可带 repo 名字/路径，如 gns sync notes）
gns sync-all     # 同步配置 repos 列表中的全部仓库
gns commit       # 提交当前修改
gns commit-ai    # AI 生成 message 后提交
gns status       # 显示工作区、远端及待处理冲突
gns resolve      # 处理已持久化的 conflict markers
gns repos        # 维护多仓库列表：list | add <path> [-name n] | del <name|path>
gns daemon       # 启动轻量 daemon（可选，Windows 首选）
```

### 4.8 技术选型（Tech Stack） · 交互层

| 项 | 选型 |
|----|------|
| 语言 | Go |
| Git 操作 | 调用系统 Git，不重新实现 |
| 覆盖范围 | CLI / 配置 / Git workflow 调度 / 可选轻量 daemon / lock file / retry / AI API 与 CLI |
| 平台 | Windows / macOS / Linux |

### 4.9 安装与分发（Installation & Distribution） · 交互层

**分发方式**：通过 npm 直接从 GitHub 仓库安装 Go 二进制，用户无需手动编译，也不依赖 npm 仓库发布。

```bash
npm install -g github:aweyonhub/git-notes-sync
```

**实现机制**（go-npm 模式，方案演进见 doc/STATUS.md「npm 分发方案踩坑记录」）：

1. **交叉编译**：Go 交叉编译各平台二进制（`windows/amd64`、`darwin/amd64`、`darwin/arm64`、`linux/amd64`、`linux/arm64`），GitHub Actions 打 tag 时自动构建并发布到 GitHub Releases，同时生成 `checksums.txt`（SHA-256 清单）。
2. **npm 包壳**：package.json 位于仓库根（`npm install github:...` 要求）；包内含 `npm/bin/gns.js`（入口 shim）与 `npm/scripts/install.js`（下载器）。
3. **postinstall 下载**：检测当前 OS / arch，从 GitHub Releases 下载对应平台二进制到 `npm/bin/gns[.exe]`——跟随 302 重定向、校验 SHA-256（`checksums.txt`，缺失降级跳过）、执行 `--version` 验证后原子落盘；支持代理（`HTTPS_PROXY`）与镜像（`GNS_RELEASE_BASE_URL`）。
4. **bin 链接 shim**：`bin` 字段指向 `npm/bin/gns.js`（Node shim，spawn 下载的二进制，缺失时给出友好提示）。
5. **allow-scripts**：npm 11+ 默认拦截 install 脚本，需 `--allow-scripts=git-notes-sync` 放行（npm 12 改为 `npm install-scripts approve` + `npm rebuild -g`）。

**平台映射示例**：

```json
{
  "darwin-arm64": "gns-darwin-arm64",
  "darwin-x64": "gns-darwin-amd64",
  "win32-x64": "gns-windows-amd64.exe",
  "linux-x64": "gns-linux-amd64",
  "linux-arm64": "gns-linux-arm64"
}
```

**优势**：用户一条 `npm install` 完成安装，无需 Go 环境、无需手动下载；npm 提供 PATH 注册与版本管理。

**安装来源**：正式版 `github:aweyonhub/git-notes-sync`（main = 最新 Release）；开发版追加 `#<临时分支>`。版本对应关系：package.json version = git tag = Release = 下载器拉取的资产版本。

---

## 五、参考与借鉴：git-auto-sync

不 fork `GitJournal/git-auto-sync`，作为工程实现参考与踩坑案例。

**值得借鉴的工程问题**：

- 定时 poll、多仓库管理；
- 可靠性：网络恢复、retry、repo 移动 / 删除、remote 不可用、lock、merge 状态处理；
- commit 频率控制：避免「每次 save 立即 commit」。

**不采用的设计**：
- 核心策略 `commit → fetch → rebase`、冲突时 abort 并停止同步。本项目采用「保护工作区 + merge + 保留冲突标记 + 继续同步」模型。
- daemon / watcher 架构。本项目不引入重型 daemon（无 watcher、无状态持久化），仅保留可选轻量 daemon 用于 Windows 定时调度与配置缓存。

---

## 六、待定问题（Open Questions）

- **是否 / 如何做到「不依赖本地 Git」**？例如内置纯 Go Git 实现，或通过远程 API 代理。当前默认仍调用系统 Git。
- **冲突批量语义解决的调度方式**：何时触发 AI、由谁触发、解决后如何回写与同步。
- **定时任务间隔的推荐值**：间隔太短浪费资源，太长延迟同步。需结合笔记场景给出默认值与调优建议。

---

## 七、实现决策（2026-08-13，开发已落地）

针对第六节待定问题与开发中的歧义，实现时采用以下决策（均可在配置中调整）：

| # | 疑问 | 决策 |
|---|------|------|
| 1 | 命令名 | Go 二进制 `gns`；npm bin 注册 `gns`（主）+ `notes-sync`（别名），文档示例按 `gns ...` 使用（2026-08-13 定） |
| 2 | 配置文件位置 | 主推全局 `~/.config/git-notes-sync/config.toml`（Windows 为 `%APPDATA%`，经 `os.UserConfigDir()`）+ `gns repos add/del` 维护多仓库，一般无需仓库级配置；仓库根 `.notes-sync.toml` 作为可选覆盖（仓库 > 全局 > 默认）；另有 `-c` 显式指定 |
| 3 | 多仓库 | `gns sync` 默认当前目录（支持按配置 repo 名字/路径定位）；daemon / `gns sync-all` 遍历配置 `repos` 列表；`gns repos list\|add\|del` 维护名单；`repos` 支持 `repos = [...]` 简单数组与 `[[repos]] name+path` 命名表两种写法；为空则当前目录 |
| 4 | debounce / max_wait 计时 | cron 无状态运行无法记住"首次发现"时间，因此在 `.git/git-notes-sync.state` 记录 first_seen：`now - mtime < debounce` 推迟；`now - first_seen >= max_wait` 强制提交（即使文件仍在编辑）。删除文件不参与 debounce |
| 5 | auto_commit=false 且工作区脏 | merge 由 git 原生拒绝覆盖本地修改；跳过该仓库并提示，不破坏工作区 |
| 6 | 二进制冲突 | 新增 `binary_strategy = "ours" \| "abort"`：ours 保留本地副本（checkout --ours）并继续；abort 中止 merge |
| 7 | 冲突策略 | `[conflict] strategy = "preserve" \| "abort"`：preserve = 保留 markers + merge commit + push；abort = merge --abort 并报错 |
| 8 | text/binary 判定 | 扩展名命中 `text_extensions` 视为文本；否则嗅探前 8KB 是否含 NUL |
| 9 | resolve 识别冲突文件 | `git grep` 匹配 `^<<<<<<< ` / `^>>>>>>> `（兼容 CRLF 行尾）+ `ls-files -u` 双路检测 |
| 10 | 冲突批量语义解决调度 | `gns resolve`（默认列出）→ `--ours` / `--theirs` / `--ai`；AI 失败保留 markers 不丢数据；解决后 add + commit + push；daemon 不做自动语义解决 |
| 11 | AI 输入大小 | `git diff --cached` 截断到 `max_diff_bytes`（默认 50KB） |
| 12 | retry | fetch/push 各 `retry_attempts`（默认 3）次，退避 2s/4s/8s；push 因远端移动被拒时自动重 fetch + 重 merge，最多 3 轮 |
| 13 | 并发锁 | `.git/git-notes-sync.lock`（O_EXCL + PID），10 分钟过期清理 |
| 14 | daemon 默认间隔 | `sync_interval = 600s`（最小 5s）；cron 建议 `*/5 * * * *` |
| 15 | 纯 Go Git 实现 | 不做，调用系统 Git（文档 §1.3 非目标）；git 封装集中在 `internal/git` 便于未来替换 |
| 16 | Git 处于 merge/rebase | 检测到 MERGE_HEAD / CHERRY_PICK_HEAD / REVERT_HEAD / rebase-* 即跳过并提示，不自动干预 |
| 17 | static 模式固定文本 | `commit_static_message`（默认 `"notes: auto sync"`） |
| 18 | AI 降级 | `ai_fallback = "timestamp" \| "static"`；ai 未配置 / 网络错误 / 超时 / 空输出均走 fallback |
| 19 | 提交身份 | 复用 git 自身 user.name/user.email；缺失时提交报错并透出 git 提示 |

**测试覆盖**（`internal/sync/engine_test.go`，真实 git 集成测试）：
快进合并与 push、文本冲突保留 + merge commit + push + `resolve --theirs` 回写远端、二进制冲突保留本地副本、debounce 推迟 / max_wait 兜底 / 静默期后提交、AI 失败 fallback、AI 未配置时 resolve 保留 markers、无 upstream 静默跳过、非仓库报错；marker 解析单测（ours/theirs/多块/未闭合/CRLF）。
