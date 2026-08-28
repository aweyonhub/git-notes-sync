---
name: git-notes-sync
description: 介绍、配置、使用和排查 git-notes-sync 项目及其 `gns`、`gnm`、`gns map`、`gns map-config` 命令。适用于讲解项目功能、安装工具、配置仓库或定时任务、同步普通 Git 工作区、处理冲突、跨机器管理 dotfile/config/skill、选择 link/copy 模式、恢复受阻的 map 流程、解释状态与日志，以及处理 Windows、macOS、Linux 和 WSL 使用问题。
---

# git-notes-sync 项目介绍与配置指南
> 项目源码：`https://github.com/aweyonhub/git-notes-sync`

---

## 一、项目概述

**git-notes-sync** 是一个面向 Git 工作区的自动同步工具（Go 实现，调用系统 Git，不重新实现 Git），包含两大功能：

| 功能 | 命令 | 定位 |
|------|------|------|
| **sync**（普通同步） | `gns` | 管理 Git 仓库的自动同步：提交 → fetch → merge → push，冲突不阻断 |
| **map**（文件映射） | `gnm` | 把本机文件（dotfiles/configs/skills/scripts）通过 worktree 映射进专用 Git 仓库，跨机器同步 |

两个功能共享同一二进制、同一调度器（daemon/cron/launchd/任务计划），`gnm` 是 `gns map` 的短命令别名（通过 argv[0] 识别）。

### 同步模型对比

| 维度 | gns sync | gnm map |
|------|----------|---------|
| 管理对象 | 普通 Git 仓库（笔记/文档等） | 本机文件（dotfiles/configs 等） |
| 冲突策略 | 保留 markers → merge commit → push（不阻断） | 唯一冲突点阻断，人工选择方向 |
| 自动同步闸门 | 无（直接同步） | `.syncable` 闸门（首次/阻断后需人工确认） |
| 文件一致性 | Git 原生 | link 模式实时强一致；copy 模式命令时收敛 |

---

## 二、安装

### npm（推荐，免编译）

```bash
# 方式一：npm registry（平台分包，零 flag）
npm install -g @aweyonhub/git-notes-sync

# 方式二：GitHub 直装（postinstall 下载器，三 flag 必需）
npm install -g --install-links=true --foreground-scripts --allow-scripts=git-notes-sync github:aweyonhub/git-notes-sync
```

安装后提供三个命令：`gns`（主命令）、`gnm`（map 短命令）、`notes-sync`（别名）。

### 手动构建（需要 Go 1.22+）

```bash
cd <项目目录>
go build -o gns ./cmd/gns          # 本机二进制（Windows 得 gns.exe，Linux/macOS 得 gns）
# 需要 gnm 时，复制/硬链接同一二进制为 gnm 即可
make cross                           # 交叉编译 6 平台；make 需类 Unix shell：
#                                     Linux/macOS 原生；Windows 在 WSL 或 Git Bash/MSYS2
#                                     中运行；或直接用 go build（版本号取源码默认）
```

### 前置要求

- **系统 Git**（`git --version` 可运行）——工具不内置 Git
- 仓库已配置远端与上游：`git remote add origin <url>` + `git push -u origin main`
- HTTPS 私有仓库：`git config --global credential.helper store` + 首次 push 存凭据

---

## 三、gns sync —— 普通仓库同步

### 3.1 快速上手

```bash
cd ~/notes                    # 进入笔记仓库
gns status                    # 查看仓库状态
gns sync                      # 手动同步：commit → fetch → merge → push

# 多仓库：注册到全局配置后用名字同步
gns repos add ~/notes -name notes
gns sync-all                  # 同步全部仓库
gns sync notes                # 同步单个
```

### 3.2 命令一览

```bash
gns sync [repo|path]          # 核心同步：commit → fetch → merge → push
gns sync-all                  # 同步配置中所有 repos
gns commit [-m "msg"]         # 立即提交（忽略 debounce）
gns commit-ai                 # AI 生成提交信息后提交
gns status [repo|path]        # 工作区/远端/冲突状态
gns resolve                   # 列出含冲突 markers 的文件
gns resolve --ours            # 全部保留本地版本，去 markers，提交推送
gns resolve --theirs          # 全部保留远端版本
gns resolve --ai              # AI 语义合并
gns repos list|add|del        # 维护多仓库列表
gns config list|get|set|unset # 查看与编辑配置
gns install [--daemon] [-interval N]  # 注册定时任务
gns uninstall                 # 卸载定时任务
gns logs [-f] [-n N]          # 查看调度器日志
gns daemon [--once]           # 轻量常驻 daemon
gns version / gns help
```

### 3.3 核心行为

- **同步流程**：可选自动提交 → 保护未提交工作区 → fetch（重试 3 次）→ merge（非 rebase）→ 文本冲突保留 markers → merge commit → push（被拒自动重 fetch+merge，最多 3 轮）
- **冲突不阻塞**：文本冲突保留双方内容与 markers → merge commit → push；冲突成为可延迟状态，事后 `gns resolve` 处理
- **提交时机**：debounce（默认 60s）防打断编辑；max_wait（默认 300s）兜底强制提交
- **提交信息**：`timestamp`（时间戳+diff 摘要）/ `static`（固定文本+摘要）/ `ai`（AI 生成，失败降级）

### 3.4 配置

配置文件位置（全局配置，`os.UserConfigDir()` 默认位置）：

| 平台 | 路径 |
|------|------|
| macOS | `~/Library/Application Support/git-notes-sync/config.toml` |
| Linux | `~/.config/git-notes-sync/config.toml` |
| Windows | `%APPDATA%\git-notes-sync\config.toml` |

自定义位置：设环境变量 `GNS_CONFIG`（支持 `~/` 展开）。

主要配置项：

```toml
auto_commit = true             # 同步前自动提交
commit_debounce = 60           # 最近修改不足 N 秒则推迟提交
commit_max_wait = 300          # 超过 N 秒强制提交
commit_message = "timestamp"   # timestamp | static | ai
binary_strategy = "ours"       # 二进制冲突：保留本地 | abort
sync_interval = 600            # daemon 轮询间隔（秒）
retry_attempts = 3             # fetch/push 重试次数
git_timeout = 120              # 单个 git 命令超时（秒）

[conflict]
strategy = "preserve"          # preserve（保留 markers）| abort
text_extensions = [".md", ".markdown", ".txt"]

[ai]                           # 可选；任何故障自动降级
type = "api"                   # api | command
base_url = "https://api.example.com/v1"
model = "model-name"
api_key_env = "NOTES_AI_API_KEY"
agent_file = "AGENTS.md"       # 仓库级 agent 指令文件

[[repos]]                      # 多仓库列表
name = "notes"
path = "~/notes"
```

### 3.5 定时调度

```bash
gns install                    # 注册定时任务
gns install --daemon           # 常驻 daemon 模式
gns install -interval 600      # 改触发间隔
gns uninstall                  # 卸载
```

| 平台 | 机制 |
|------|------|
| macOS | launchd LaunchAgent：默认 `StartInterval` 定时触发；`--daemon` 用 `KeepAlive` 常驻 |
| Linux | systemd user units（默认）/ crontab（--cron） |
| Windows | 任务计划程序（schtasks，无需管理员，每分钟触发） |

多套注册用 `--label` 隔离：`gns install --label work -interval 300`。

---

## 四、gnm map —— 本机文件映射同步

### 4.1 功能定位

`map` 把分散在不同机器上的 dotfile、config、skill、脚本和目录统一纳入一个专用 Git 仓库，跨机器同步而不改变文件日常使用方式（软链接或增量复制）。

### 4.2 核心概念

| 概念 | 说明 |
|------|------|
| **git-root** | 用户手动准备的专用集成仓库，始终通过 pull 与远程保持一致 |
| **map-root** | 当前机器在仓库中的命名空间（如 `winTx`、`mba-m4`） |
| **worktree** | 每台机器的本地工作分支 `gns/map/<map-root>-worktree` |
| **`.syncable`** | 自动同步闸门，首次/阻断后需人工 push 才创建/恢复 |
| **link 模式** | 本机文件变为指向 worktree 的符号链接（实时强一致） |
| **copy 模式** | 增量复制（size+mtime 过滤，命令时收敛） |

### 4.3 映射模式

| 模式 | 行为 | 适用平台 |
|------|------|---------|
| `auto` | Windows→copy，Linux/macOS→link（默认） | 所有 |
| `link` | 符号链接，创建失败报错不降级 | Linux/macOS 原生；Windows 需开发者模式或管理员 |
| `copy` | 增量复制，不做 watcher | 所有 |

> **Windows link 前提**：需开启"开发者模式"（设置→系统→开发者选项）或以管理员运行。开发者模式一次开启永久生效，普通终端即可创建 symlink。已开启开发者模式时**推荐使用 link 模式**，获得零复制的实时编辑体验（`gns config set map.mode link`）；未开启或无管理员权限时改用 copy。

### 4.4 配置

```bash
gnm config git-root <path>          # 指向集成仓库
gnm config map-root <name>          # 设置机器命名空间
gns config set map.mode link        # auto | link | copy
gnm config add -a <repo> <local>    # scope=map-root（机器专属，存到 <map_root>/ 下）
gnm config add -A <repo> <local>    # scope=git-root（公共区，跨机器共享）
gnm config remove <local-path>      # 按本机路径解除映射
gnm config remove -A                # 解除全部
gnm config list                     # 查看有效映射
gnm config validate                 # 检查问题
gnm config save [<map-root>]        # 写配置快照到 worktree
gnm config load [<map-root>]        # 从 git-root HEAD 导入配置（仅 init 前）
```

配置示例（写入全局 config.toml 的 `[map]` 段）：

```toml
[map]
git_root = "E:/map-repo"
map_root = "winTx"
mode = "auto"
sync = false                   # true 时调度器每轮额外执行 gns map sync

[[map.items]]
scope = "map-root"             # 存到 winTx/ 前缀下
path = "pi-skill"
local_path = "~/.pi/skills"

[[map.items]]
scope = "git-root"             # 公共区
path = "common/.bashrc"
local_path = "~/.bashrc"
```

### 4.5 首次使用流程

```bash
# 1. 准备 git-root 仓库（用户手动 clone/创建，需有 upstream）
git clone <remote> ~/map-repo && cd ~/map-repo && git push -u origin main

# 2. 配置
gnm config git-root ~/map-repo
gnm config map-root winTx
gnm config add -a bashrc ~/.bashrc
gnm config add -a gitconfig ~/.gitconfig
gnm config add -A common/skill-a ~/.config/skill-a.md

# 3. 初始化
gnm init                       # 创建 worktree + 应用映射（不创建 .syncable）

# 4. 审查 + 首次确认
gnm status                     # 查看差异
gnm add -A                     # 所有映射采用本机版本
# 或 gnm add ~/.bashrc         # 逐个选择
# 或 gnm get ~/.bashrc         # 采用 HEAD 版本
gnm commit -m "initialize map"
gnm push                       # 首次成功后创建 .syncable

# 5. 开启自动同步（初始化的最后一步）
gns config set map.sync true   # 现有调度器每轮额外执行 gns map sync
```

### 4.6 日常命令

```bash
gnm status                     # 三状态 + 映射事实 + 下一步命令
gnm add <path|pattern...>      # 选择本机版本并暂存
gnm get <path|pattern...>      # 选择 HEAD 版本并下发
gnm add -A / gnm get -A        # 所有映射
gnm commit [-m "msg"]          # 提交已暂存内容
gnm push                       # 人工确认推送（arms .syncable）
gnm sync                       # 自动同步（需 .syncable）
gnm pull [-f|--force]          # 阻断恢复入口
```

三状态：

| 状态 | 含义 | 下一步 |
|------|------|--------|
| `NOT_INITIALIZED` | worktree 未创建 | `gnm init` |
| `MANUAL_REQUIRED` | 首次确认或阻断恢复中 | `gnm add/get` → `commit` → `push` |
| `SYNCABLE` | 允许自动同步 | `gnm sync` 或等待调度 |

### 4.7 自动同步

```bash
gns config set map.sync true   # 启用后现有调度器每轮额外执行 gns map sync
```

不需要单独安装 map 定时任务——复用 gns 的 daemon/cron/launchd/任务计划。

### 4.8 冲突与阻断恢复

**worktree 合并冲突**（唯一冲突点）：

```bash
gnm pull                       # worktree reset --mixed（不动本机文件）
gnm status                     # 查看冲突文件
gnm add <path>                 # 保留本机版本
# 或 gnm get <path>            # 采用 HEAD 版本
gnm commit -m "resolve map conflict"
gnm push                       # 重新 arm .syncable
```

**git-root 与远程历史分叉**：

```bash
gnm pull --force               # git-root reset --hard 对齐 upstream（worktree 用 reset --mixed）
gnm status
gnm add <path>                 # 或 gnm get <path>
gnm commit -m "resolve map divergence"
gnm push
```

`.syncable` 删除条件（进入 MANUAL_REQUIRED）：
- worktree 合并 git-root 发生内容冲突
- 映射根只在一侧存在或两侧类型不同
- git-root 无法 fast-forward
- 有效映射配置变化（add/remove/load）

网络/认证/权限等可重试错误**不删除** `.syncable`，下次同步重新判断。

### 4.9 安全边界

- worktree **禁止 `reset --hard`**（会覆盖真实文件）；`--force` 只对 git-root 用
- link 模式不删除未确认属于当前映射的真实文件
- copy 模式临时文件 + 原子替换，保留权限与 mtime
- 旧 HEAD 通过备份 ref `refs/gns/map/<map-root>-backup` 保持可恢复
- 移动 worktree HEAD 前记录旧 commit

### 4.10 跨平台说明

| 问题 | 结论 |
|------|------|
| Windows link 模式 | 需开发者模式或管理员；`auto` 默认选 copy 避开限制；已开启开发者模式推荐显式 `link`（零复制实时编辑） |
| 跨盘 symlink | 支持（NTFS 原生） |
| WSL 文件管理 | Windows gns **不能**通过 UNC 路径操作 WSL git 仓库（`failed to get owner`）；需 WSL 内单独部署 Linux 版 gns |
| 多环境部署 | 各环境管各环境的原生文件，共享同一远程仓库，用不同 map-root 隔离 |

---

## 五、目录布局

### gns 配置/日志

```
macOS:   ~/Library/Application Support/git-notes-sync/config.toml
         ~/Library/Logs/com.git-notes-sync.log
Linux:   ~/.config/git-notes-sync/config.toml
         ~/.local/state/git-notes-sync/com.git-notes-sync.log
Windows: %APPDATA%\git-notes-sync\config.toml
         %LOCALAPPDATA%\git-notes-sync\com.git-notes-sync.log
```

### gnm map 状态

```
<gns-app-data>/map/
├── <map-root>/                  # 状态目录
│   ├── .syncable                # 自动同步闸门
│   ├── blocked.json             # 阻断原因
│   └── git-notes-sync.lock      # map 锁
└── <map-root>-worktree/         # 机器 worktree
    └── .git
```

`<gns-app-data>` 默认跟随系统用户配置目录，可用 `GNS_APP_DATA` 环境变量覆盖。

---

## 六、开发

```bash
make build          # 本机二进制
make cross          # 交叉编译 6 平台
make test           # 集成测试（需要系统 git）
go test ./internal/mapsync/...  # map 测试（真实 git 集成）
```

代码结构：

```
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
