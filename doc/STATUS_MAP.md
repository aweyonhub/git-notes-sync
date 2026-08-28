# GNS Map 开发状态

> 面向 v0.1.7 的 map 实现状态。功能设计见 [git-notes-sync_map.md](./git-notes-sync_map.md)，用户操作见 [USAGE_MAP.md](./USAGE_MAP.md)。普通 sync 功能状态见 [STATUS.md](./STATUS.md)。

---

## 一、功能状态

### 1.1 已完成

| 模块 | 当前实现 | 状态 |
|---|---|---|
| 配置模型 | `[map]`、`[[map.items]]`、`git-root`/`map-root`/`mode`/`sync` | ✅ |
| 命令入口 | `gns map`、`gns map-config`、短命令 `gnm` | ✅ |
| worktree | 固定机器分支与目录；基于 git-root 当前 HEAD 初始化 | ✅ |
| 映射模式 | `auto`、`link`、`copy`；Windows auto→copy，Linux/macOS auto→link | ✅ |
| 配置操作 | add/remove/list/validate/save/load；初始化后增删立即生效 | ✅ |
| 内容选择 | `gnm add/get` 支持 mapped 文件/目录、`-A/--all`、单层 `*`，以及非映射 worktree 文件的精确路径 | ✅ |
| 状态诊断 | 三状态、Git 状态、映射关系与方向、dirty 文件清单、copy 本机变化、推荐操作 | ✅ |
| 路径导航 | `gnm cd worktree` / `gnm cd git-root` 输出可供 shell 接管的纯路径 | ✅ |
| 同步链路 | pull → worktree merge → git-root fast-forward → push | ✅ |
| 阻断恢复 | 普通 pull、force pull、备份 ref、人工 add/get/commit/push | ✅ |
| 自动闸门 | 首次 push 创建 `.syncable`；明确人工边界时解除 | ✅ |
| 调度复用 | `map.sync=true` 后复用现有 daemon/cron/系统定时任务 | ✅ |

### 1.2 安全边界

当前实现遵守以下核心约束：

- 不对机器 worktree 使用 `reset --hard`；
- link 模式不删除未确认属于当前映射的真实文件；
- copy 模式使用临时文件和原子替换，并保留权限与 mtime；
- 映射根新增、删除或类型变化方向不明确时阻断；
- 只有确定需要人工重新选择内容或历史时才删除 `.syncable`；
- git-root 无法 fast-forward 时停止，不在 git-root 再做一次内容合并；
- add/remove 文件操作失败时回滚配置并恢复本机路径（link 解除采用暂存-复制-清理事务）；worktree 侧可能留下普通 Git 修改，由下一次同步收敛；
- `status` 在未配置、首次初始化和阻断状态下都给出下一步命令。

## 二、代码结构

| 位置 | 职责 |
|---|---|
| `internal/mapsync/model.go` | 映射模型、路径和包含关系校验 |
| `internal/mapsync/paths.go` | app-data、状态目录、worktree、分支和备份 ref 命名 |
| `internal/mapsync/configops.go` | map 配置编辑、校验及快照 save/load |
| `internal/mapsync/fsops.go` | link/copy 文件操作、元数据与并发变化保护 |
| `internal/mapsync/init.go` | worktree 初始化及映射建立/解除 |
| `internal/mapsync/addget.go` | add/get/commit 选择流程 |
| `internal/mapsync/engine.go` | push/sync 共享集成链路 |
| `internal/mapsync/pull.go` | 普通与 force 阻断恢复 |
| `internal/mapsync/status.go` | 状态事实和推荐命令 |
| `internal/mapsync/state.go` | Env、`.syncable` 和 blocked state |
| `internal/cli/mapcmd.go` | `gns map`、`gns map-config`、`gnm` CLI |

## 三、测试覆盖

### 3.1 已覆盖场景

- init → add/get → commit → push → sync 完整流程；
- 远端并发修改、worktree 合并冲突和恢复；
- force pull 对齐远程，同时保持本机真实文件；
- `.syncable` 首次确认、阻断解除和可重试错误保留；
- link 模式生命周期和未受管理文件保护；
- copy 模式 size/mtime/权限复制、目录删除及并发变化保护；
- 配置 add/remove 失败回滚；
- 通配符、精确路径、映射根删除确认；
- 未配置状态及各种状态提示。

### 3.2 测试特征

mapsync 集成测试会创建真实 bare remote、clone 和 Git worktree，并执行真实 commit/merge/push。覆盖面较完整，但在 Windows 上运行时间明显高于纯单测；不为缩短几十秒引入共享仓库夹具，保持每个测试仓库独立。

## 四、当前保留行为

以下不是缺陷，当前不计划修改：

- copy 模式只比较 size、mtime 和权限，不持久化 hash；同内容但 mtime 变化会保守地重新复制或要求重新 add；
- `gnm status` 是用户主动触发的完整诊断，允许递归扫描大型映射目录；
- 网络、认证、权限和普通 push rejected 默认保留 `.syncable`，等待下次同步重新判断；
- pull 失败后通过 HEAD 与 upstream 的提交图确认是否真正分叉，不依赖 Git 错误文案；
- map 不创建仓库、不选择固定主分支、不替用户决定远程或复杂 Git 历史；
- map 不实现 watcher，也不单独实现 daemon 或定时任务。
- `gnm status` 是显式执行的诊断命令，允许按映射查询 HEAD；普通同步链路不为此维护额外缓存。

## 五、待实现功能（P0）

| 问题 | 位置 | 说明 |
|------|------|------|
| map 映射 ignore 规则 | `internal/config/config.go:67-71`、`internal/mapsync/configops.go:109-111`、`internal/mapsync/fsops.go:104-190`、`internal/mapsync/status.go:125-136` | 映射目录时按规则忽略子项——如 `.codex/skills` 里的 `.system`（系统内置 skill）不映射，只同步 `vip-token-pi` 等自定义项。`[[map.items]]` 增加 ignore 字段（glob，类 `.gitignore`）；copy/link 模式 SyncTree 遍历与删除传播跳过匹配项；`gnm status` 对忽略项不报差异。 |

v0.1.7 已完成原表 #1/#2/#4/#5：Next 分支、mapping 关系与方向、dirty 文件清单、非映射 worktree 文件的精确 add/get、changes 的 local 可执行路径，以及 mapping 级 staged/remaining 进度。Review 与修复记录见 [b017 code review](./review/b017-7b28265.md)。

## 六、`gnm status` 改版设计（v0.1.7）

### 6.1 格式定义

映射行：

```
<本地路径> [本地状态] (scope) <仓库路径> [远程状态] [TO xxx]
```

- 状态方括号 `[dir]` / `[file]` / `[link]` / `[missing]` **只在两端不一致时显示**；一致则省略
- scope 与仓库路径之间用空格分隔（无箭头）
- 推荐操作 `[TO xxx]`：大写 `TO` + 小写动词
- mapped 改动行（dirty 具体文件，列在映射行下）：`<本地完整路径> [repo: <仓库路径>] [TO xxx]`
- 非映射改动行：`<worktree 相对路径> [other] [TO xxx]`

### 6.2 推荐操作词汇表

| 标记 | 含义 | 对应命令 |
|------|------|---------|
| `[TO add]` | 采用本机版本 | `gnm add <path>` |
| `[TO get]` | 采用仓库版本 | `gnm get <path>` |
| `[TO add OR get]` | 方向不明，需人工选 | `gnm add` / `gnm get` |
| `[TO commit]` | 已选择，待提交 | `gnm commit` |
| `[TO push]` | 已提交，待推送 | `gnm push` |

### 6.3 各阶段示例

**首次 MANUAL_REQUIRED（两端不一致）**
```
state:      MANUAL_REQUIRED
map-root:   vip-desktop
mode:       link
git-root:   E:\aWEY\github\awey-map  main @ 380d1382be
worktree:   D:\...\vip-desktop-worktree  gns/map/vip-desktop-worktree @ 380d1382be
.syncable:  false

mappings:
  ~/.dsh/skills [dir] (map-root) ai/dsh-skills [missing]  [TO add]
  ~/.pi/agent/skills [dir] (map-root) ai/pi-skills [missing]  [TO add]

Next: gnm add -A
Then: gnm commit ; gnm push
```

**add 了 pi 之后（进度变化）**
```
mappings:
  ~/.dsh/skills [dir] (map-root) ai/dsh-skills [missing]  [TO add]
  ~/.pi/agent/skills (map-root) ai/pi-skills  [TO commit]

Next: gnm add ~/.dsh/skills
```

**SYNCABLE（一致，干净）**
```
mappings:
  ~/.dsh/skills (map-root) ai/dsh-skills
  ~/.pi/agent/skills (map-root) ai/pi-skills

Next: nothing required
```

**SYNCABLE（dirty，有单独文件）**
```
mappings:
  ~/.dsh/skills (map-root) ai/dsh-skills
  ~/.pi/agent/skills (map-root) ai/pi-skills

改动:
  .gns/map/vip-desktop.toml [other] [TO add]          ← 未跟踪快照文件
  .gitignore [other] [TO add OR get]                  ← 已跟踪文件的 unstaged 修改，方向需人工选择

Next: gnm sync
```

**mapping-root（方向不明）**
```
mappings:
  ~/.dsh/skills [dir] (map-root) ai/dsh-skills [file]  [TO add OR get]

Next: gnm add ~/.dsh/skills   # 采用本机
  或  gnm get ~/.dsh/skills   # 采用仓库
```

**merge-conflict（冲突有具体文件）**
```
blocked:    merge-conflict
冲突:
  ~/.dsh/skills/herdr/SKILL.md  [TO add OR get]

Next: gnm add ~/.dsh/skills/herdr/SKILL.md
  或  gnm get ~/.dsh/skills/herdr/SKILL.md
```

### 6.4 路径跳转命令

`gnm cd <worktree|git-root>` 输出目标目录的绝对路径，方便用户在 shell 里跳转后手动操作（worktree 路径藏在 app-data 里很长）：

```
gnm cd worktree   # → D:\Users\...\AppData\Roaming\git-notes-sync\map\vip-desktop-worktree
gnm cd git-root   # → E:\aWEY\github\awey-map
```

用法（子进程不能直接改父 shell 的 cwd，所以输出路径由 shell 接手）：

```powershell
# PowerShell
cd (gnm cd worktree)
cd (gnm cd git-root)
```
```bash
# bash / zsh
cd "$(gnm cd worktree)"
cd "$(gnm cd git-root)"
```

约束：只输出纯路径一行（无日志、无 `map ...` 前缀），否则 shell 命令替换会拿到脏内容。
