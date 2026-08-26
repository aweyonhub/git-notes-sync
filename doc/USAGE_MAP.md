# GNS Map 使用说明

> `map` 将不同机器上的 dotfile、config、skill、脚本和目录映射到一个专用 Git 仓库。完整设计与安全边界见 [git-notes-sync_map.md](./git-notes-sync_map.md)，普通仓库同步见 [USAGE.md](./USAGE.md)。

---

## 1. 命令入口

`gnm` 是 map 功能的短命令，由安装产物直接提供：

```text
gnm config ... = gns map-config ...
gnm <command>  = gns map <command>
```

查看帮助：

```bash
gnm help
gnm config help
```

## 2. 使用前准备

map 不负责创建远程仓库、选择分支或处理仓库自身的复杂历史。开始前需要准备一个由用户管理的 git-root：

```bash
git clone <remote-url> ~/map-repo
cd ~/map-repo
git switch <branch>
git push -u origin <branch>   # 当前分支需要 upstream
```

git-root 必须满足：

- 是已有提交的 Git 仓库；
- 当前处于普通分支，而不是 detached HEAD；
- 工作区干净；
- 当前分支已配置 upstream。

map 会使用 git-root 的当前分支和当前 HEAD，不写死 `main`。

## 3. 首次配置与初始化

### 3.1 基础配置

```bash
gnm config git-root ~/map-repo
gnm config map-root winTx
gns config set map.mode auto
```

- `git-root`：用户管理的集成仓库。
- `map-root`：当前机器在仓库中的命名空间，例如 `winTx`、`mba-m4`、`tx-wsl-de13`。
- `mode`：`auto`、`link` 或 `copy`。`auto` 在 Windows 上解析为 `copy`，在 Linux/macOS 上解析为 `link`。

worktree 已初始化且仍存在映射项时，不能修改这三个基础字段。需要先执行：

```bash
gnm config remove -A
```

### 3.2 添加映射

机器专属映射使用 `-a`，仓库公共映射使用 `-A`：

```bash
gnm config add -a pi-skill ~/.pi/skills
gnm config add -a .bashrc ~/.bashrc
gnm config add -A common/.bashrc ~/.bashrc
```

对应关系：

| 参数 | scope | 仓库路径示例 |
|---|---|---|
| `-a` | `map-root` | `winTx/pi-skill` |
| `-A` | `git-root` | `common/.bashrc` |

本机路径是映射的唯一标识。删除映射时直接提供本机路径：

```bash
gnm config remove ~/.bashrc
gnm config remove ~/.pi/skills
gnm config remove -A          # 解除全部映射
```

初始化前，`config add/remove` 只修改用户配置；初始化后会立即建立或解除对应文件映射，并删除 `.syncable`，要求用户重新确认。

### 3.3 初始化

```bash
gnm init
```

初始化会：

1. 从 git-root 当前 HEAD 创建机器分支 `gns/map/<map-root>-worktree`；
2. 在 `<gns-app-data>/map/<map-root>-worktree` 创建 worktree；
3. 将当前机器的 map 配置写入 `.gns/map/<map-root>.toml`；
4. 对现有映射执行一次初始化；
5. 保持 `.syncable` 不存在，等待首次人工确认。

本机和 worktree 都存在内容时，本机版本先写入 worktree。link 模式随后把本机路径替换为指向 worktree 的软链接；copy 模式保留两份文件并按 size、mtime 和权限增量复制。

## 4. 首次人工确认

初始化后先查看状态：

```bash
gnm status
```

对每个差异选择一侧：

```bash
gnm add ~/.bashrc       # 采用本机版本：local → worktree → index
gnm get ~/.bashrc       # 采用 HEAD 版本：HEAD → index/worktree → local

gnm add -A              # 所有映射采用本机版本
gnm get -A              # 所有映射采用 HEAD 版本
```

支持单层 `*` 通配符；建议加引号，避免由 shell 提前展开：

```bash
gnm add "~/.pi/*"
gnm get "~/.config/*"
```

完成选择后提交并推送：

```bash
gnm commit -m "initialize map"
gnm push
```

第一次 `gnm push` 完整成功后才创建 `.syncable`，允许自动同步。

## 5. 日常命令

### 5.1 查看状态

```bash
gnm status
```

状态只有三种：

| 状态 | 含义 | 常用下一步 |
|---|---|---|
| `NOT_INITIALIZED` | worktree 尚未创建 | `gnm init` |
| `MANUAL_REQUIRED` | 首次确认或阻断恢复中 | `gnm add/get`、`commit`、`push` |
| `SYNCABLE` | 允许自动同步 | `gnm sync` 或等待调度 |

`status` 会显示 git-root、worktree、映射两侧类型、暂存状态、copy 模式尚未刷新的本机变化，以及当前推荐命令。

### 5.2 手动选择、提交和推送

```bash
gnm add <path-or-pattern...>     # 采用本机版本并暂存
gnm get <path-or-pattern...>     # 采用 HEAD 版本并下发
gnm commit                       # 使用默认提交信息
gnm commit -m "message"         # 自定义提交信息
gnm push                         # 人工确认入口
```

`gnm push` 不会替用户隐式暂存或提交尚未处理的内容。正常链路为：

```text
git-root pull --ff-only
→ worktree merge git-root
→ git-root fast-forward 到 worktree HEAD
→ git-root push
```

### 5.3 自动同步

```bash
gnm sync
```

只有 `.syncable` 存在时才会运行。copy 模式先增量刷新本机内容，随后自动执行 `add -A`、必要的 commit、合并和 push。

启用现有调度器中的 map 同步：

```bash
gns config set map.sync true
```

GNS 现有 daemon、cron、systemd timer、launchd 和 Windows 任务计划会在每轮调度时额外运行一次 map sync，不需要单独安装 map 调度任务。

## 6. 配置快照

```bash
gnm config save [<map-root>]
gnm config load [<map-root>]
```

- `save`：只把当前 `[map]` 配置写入 worktree 的 `.gns/map/<map-root>.toml`，不自动暂存、提交或推送。worktree 未初始化时不会立即写入；首次 `init` 会创建初始快照。
- `load`：初始化前从 git-root 当前 HEAD 读取指定机器的配置快照。省略参数时使用当前配置中的 `map_root`。
- `load` 只修改用户配置，不创建 worktree，也不覆盖本机文件；随后执行 `gnm init` 才应用映射。

手动发布配置快照：

```bash
gnm config save
gnm add -A
gnm commit -m "update map config"
gnm push
```

## 7. 冲突和阻断恢复

### 7.1 worktree 合并冲突

```bash
gnm pull
gnm status
gnm add <path>       # 保留本机版本
# 或
gnm get <path>       # 采用 git-root/HEAD 版本
gnm commit -m "resolve map conflict"
gnm push
```

`gnm pull` 会把机器分支 HEAD 和 index 移到 git-root 当前 HEAD，同时保持 worktree 文件和本机真实文件不变，等待用户选择。

### 7.2 git-root 与远程历史分叉

```bash
gnm pull --force
gnm status
gnm add <path>       # 或 gnm get <path>
gnm commit -m "resolve map divergence"
gnm push
```

`--force` 只允许 git-root 强制对齐 upstream；不会对承载本机文件的 worktree 执行 `reset --hard`。旧 HEAD 会通过备份 ref 保持可恢复。

### 7.3 `.syncable` 规则

以下情况会进入 `MANUAL_REQUIRED` 并删除 `.syncable`：

- worktree 合并 git-root 发生内容冲突；
- 映射根只在一侧存在或两侧类型不同，无法自动判断新增/删除方向；
- 本机文件在同步期间发生并发变化；
- git-root 或机器分支确认无法 fast-forward。

普通网络、认证、权限或 push rejected 等可重试错误不会自动删除 `.syncable`；下一次同步会重新判断。

## 8. 文件同步规则

### link 模式

- 本机路径是指向 worktree 文件或目录的软链接；
- 日常编辑直接发生在 worktree 文件上；
- 建立链接前会保留本机内容，拒绝覆盖未受管理的真实文件；
- Windows 需要具备创建软链接的权限，失败时不会静默降级为 copy。

### copy 模式

- 不使用 watcher，只在 `add`、`get`、`sync` 等命令执行时刷新；
- 文件 size 与 mtime 相同则跳过复制，不计算内容 hash；
- 复制保留文件和目录权限、mtime；
- 目录同步会传播源侧删除；映射根级的新增/删除方向必须由 `add/get` 明确选择；
- `gnm status` 会主动递归比较本机与 worktree，以提供准确诊断。

## 9. 本机目录布局

```text
<gns-app-data>/map/<map-root>/
├─ .syncable
├─ blocked.json
└─ git-notes-sync.lock

<gns-app-data>/map/<map-root>-worktree/
└─ .git
```

默认 app-data 位置跟随系统用户配置目录，也可以用 `GNS_APP_DATA` 覆盖。不要手工删除 worktree 的 `.git` 或修改 git-root 中的 worktree 注册信息。
