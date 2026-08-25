# Git Notes Sync Map

> `map` 功能需求与设计草案。
>
> 本文描述目标、数据模型、命令职责和同步流程。命令名称及部分配置字段仍可在实现前调整，但以下安全边界和处理原则应保持不变。

---

## 一、功能定位


`map` 通过映射将本机文件纳入 Git 仓库，把分散在不同机器上的 dotfile、config、skill、脚本和目录等内容统一管理。它采用简化的数据模型，在不改变文件日常使用方式的前提下，支持从仓库下发、向仓库上传以及定时同步。

~~~text
远程仓库
        ↓ git-root 通过pull 定时与远程**强一致**
机器 git-root 当前分支 / 当前 HEAD
        ↕ gnm push 或 gnm sync 触发合并,**唯一可能有冲突**的地方需要人工解决
机器 worktree HEAD:
        ↕ add/get/commit等命令选择并提交本机内容
机器 worktree 文件: 
        ↕ 通过软连接或者copy机制与本地文件**强一致**
本机真实文件
~~~

设计原则：

- **职责独立**：`map` 是独立于 `sync` 的功能。`map` 负责本机文件映射与同步，遇到冲突时阻断并由用户手动解决；`sync` 负责普通 Git 仓库同步，并按自身策略自动处理冲突。
- **本机文件模型**：在逻辑上，本机文件就是 worktree 的真实工作文件；软链接或复制机制只负责让两者保持强一致。
- **远程一致性**：`git-root` 通过定时 `pull` 与远程仓库保持一致，worktree 不直接承担远程拉取职责。
- **唯一 Git 内容冲突点**：只有 worktree 分支合并 `git-root` 当前分支时可能产生需要人工解决的 Git 内容冲突。映射根选择和 Git 状态异常也可能暂停自动同步，但不属于内容冲突。
- **正常同步流程**：两者都先执行 `git-root pull`；`gnm push` 确认用户已经选择并提交 worktree 内容，`gnm sync` 自动执行 `add -A + commit`；随后执行 `worktree merge git-root（可能发生内容冲突）→ git-root fast-forward 到 worktree HEAD → git-root push`。
- **阻断恢复流程**：worktree 合并冲突后使用 `gnm pull`；git-root 与远程无法 fast-forward 时使用 `gnm pull -f/--force`，强制以远程分支对齐 git-root，再将 worktree HEAD 和 index 切换到新基准。整个过程保持本机真实文件不变；用户通过 `gnm add` 或 `gnm get` 选择内容，执行 `gnm commit` 提交，再通过 `gnm push` 恢复正常同步。
- **自动同步闸门**：首次 `init` 后不允许立即自动同步。用户处理首次差异并成功手动 `gnm push` 后创建 `.syncable`；发生冲突时删除该文件。只有用户完成阻断处理、手动选择内容并再次成功同步后，才重新创建 `.syncable`，恢复定时自动提交和同步。
- **复用现有调度**：`map` 不单独实现或注册定时任务与 daemon，而是复用 GNS 现有的 cron、系统定时任务和 daemon。`map.sync` 默认为 `false`；用户执行 `gns config set map.sync true` 后，现有调度器在每轮触发时额外执行 `gns map sync`。这是 `map` 与 GNS 共用同一项目和运行时的核心原因之一。

## 二、核心模型

### 2.1 四个角色

- **远程仓库**：跨机器共享的最终 Git 历史。
- **git-root**：当前机器上的集成仓库，始终通过 `pull` 与远程仓库保持一致；用户不直接在其中维护机器专属修改。
- **worktree 分支**：当前机器的本地工作分支，固定命名为 `gns/map/<map-root>-worktree`，负责承载本机文件修改。
- **本机真实文件**：用户和程序实际读写的 dotfile、config、skill、脚本或目录。

### 2.2 两组强一致关系

本设计中的“强一致”强调两端不保留需要合并的独立工作状态，而是通过固定方向直接收敛，因此不会在这两层产生内容冲突或阻断。它不要求 copy 模式在任意时刻都实时相同：link 模式实时一致，copy 模式在命令或定时任务执行后收敛，时间上属于最终一致，但同步语义仍是确定方向的强制一致。

**远程仓库 ↔ git-root**

- git-root 是远程当前分支的本机集成副本；
- git-root 不承载独立的机器修改；
- 定时 `pull` 负责把远程状态更新到 git-root；
- 正常自动流程只允许 fast-forward；一旦无法 fast-forward 则阻断，由用户通过 `gnm pull --force` 强制以远程分支恢复一致；
- 网络失败只会推迟本次同步，不形成需要人工选择内容的冲突。

**worktree 文件 ↔ 本机真实文件**

- 软链接模式下，两者实际指向同一份内容；
- copy 模式下，GNS 按命令或定时任务执行确定方向的覆盖并最终收敛；
- 正常提交前执行“本机 → worktree”；
- Git 内容下发后执行“worktree → 本机”；
- 两者不做 Git 式合并，也不产生双边冲突状态。

### 2.3 唯一冲突点

唯一需要人工处理的内容冲突发生在：

~~~text
worktree 分支 merge git-root 当前 HEAD
~~~

这是机器专属修改与远程最新内容第一次真正汇合的位置。

worktree 成功合并 git-root 后，worktree 已包含 git-root 的全部历史，因此后续：

~~~text
git-root merge worktree
~~~

这一步只能 fast-forward，不再执行内容合并。fast-forward 失败属于异常：报告错误、删除 `.syncable` 并停止，等待用户处理。

### 2.4 worktree 创建

首次初始化时，从 git-root 当前 HEAD 创建机器分支和 worktree：

~~~powershell
git worktree add -b gns/map/<map-root>-worktree <gns-app-data>/map/<map-root>-worktree HEAD
~~~

基准不要求固定为 `main`。git-root 当前 checkout 的分支和 HEAD 就是本次初始化基准。

worktree 默认位于：

~~~text
<gns-app-data>/map/<map-root>-worktree
~~~

分支名和目录名采用固定规则：

~~~text
本地分支：gns/map/<map-root>-worktree
本地目录：<gns-app-data>/map/<map-root>-worktree
~~~

两者均为当前机器的本地状态。worktree 分支不作为机器间共享分支直接推送，而是合并到 git-root 当前分支后，由 git-root 统一 push。名称中的 `map-root` 来自用户配置，并必须经过路径和 Git ref 合法性校验。

## 三、配置设计

### 3.1 命令职责

配置定义与运行操作分开：

~~~text
gnm config ...        # gns map-config：定义映射关系、保存和加载配置
gnm ...               # gns map：初始化、提交、同步和阻断恢复
~~~

`gnm config` 负责映射定义及单项映射的建立和移除，不创建整个 worktree；`gnm` 的其他命令负责 worktree、本机文件和 git-root 的整体状态流转。worktree 尚未初始化时，`config add/remove` 只修改用户配置；初始化后，它们会立即应用对应映射的建立或移除逻辑。

### 3.2 基础配置

~~~text
gnm config git-root <path>
gnm config map-root <name>
~~~

- `git-root`：用户手动准备的 map Git 仓库；
- `map-root`：当前机器在仓库中的命名空间，例如 `winTx`、`mba-m4`、`tx-wsl-de13`。
- worktree 已初始化且仍有映射项时，禁止修改 `git-root`、`map-root` 或映射模式；必须先执行 `gnm config remove -A` 完整解除所有映射，否则报错并保持现状。

### 3.3 映射配置

~~~text
gnm config add -a <map-path> <local-path>
gnm config add -A <repo-path> <local-path>
gnm config remove <local-path...>
gnm config remove -A | --all
gnm config list
gnm config validate
~~~

路径语义：

- `-a`：保存为 `scope = "map-root"`。例如 `-a .bashrc ~/.bashrc` 在 `map_root = "winTx"` 时解析为 `winTx/.bashrc`；
- `-A`：保存为 `scope = "git-root"`。例如 `-A common/.bashrc ~/.bashrc` 解析为 `common/.bashrc`；
- 配置保留 `scope + path`，运行时根据当前 `map_root` 解析实际仓库路径；
- `path` 必须是规范化的仓库相对路径，禁止绝对路径和 `..` 越界；
- `local-path` 是映射的唯一标识，`remove` 只接受映射根的精确本机路径，不接受子路径或通配符；
- 运行时将 `~` 展开、相对路径按当前工作目录转为绝对路径，再清理 `.` 和 `..`；该过程不解析软链接目标；
- 配置中的 `~/...` 保留为可跨机器解析的形式，普通绝对路径保存规范化结果；
- 路径比较在 Windows 上忽略大小写，在 Linux/macOS 上区分大小写；
- 配置校验同时检查规范化后的本机路径和解析后的仓库路径，两侧都禁止重复或互相包含的映射。

映射模式：

| 配置值 | 行为 |
|---|---|
| `auto` | 默认值；Windows 使用 `copy`，Linux 和 macOS 使用 `link` |
| `link` | 显式使用软链接，创建失败时报告错误，不静默切换为 copy |
| `copy` | 显式使用增量复制，不创建 watcher |

`auto` 在 `init` 时解析为实际模式，后续保持不变；`gnm status` 应同时展示配置值和实际值，例如 `mode: auto → copy`。

配置示例（仅以 `map-root = "winTx"` 演示，实际值来自用户配置）：

~~~toml
[map]
git_root = "E:/map-repo"
map_root = "winTx"
mode = "auto"
sync = false

[[map.items]]
scope = "map-root"
path = "pi-skill"
local_path = "~/.pi/skills"

[[map.items]]
scope = "git-root"
path = "common/.bashrc"
local_path = "~/.bashrc"
~~~

映射不保存 `kind`，文件、目录或软链接类型以当前实际内容为准。两侧都不存在时保持未知且不创建内容；以后首次出现时再根据实际类型处理。

### 3.4 配置快照

仓库中保存每台机器的配置副本：

~~~text
map-git-root/
├─ .gns/
│  └─ map/
│     ├─ <map-root>.toml
│     └─ <other-map-root>.toml
├─ common/
├─ <map-root>/
└─ <other-map-root>/
~~~

相关命令：

~~~text
gnm config save [<map-root>]
gnm config load [<map-root>]
~~~

- map-root 的取值优先级为“命令行参数 → 当前用户配置中的 `map_root`”；例如未传参数且配置为 `map_root = "winTx"` 时，两条命令都操作 `winTx`；
- `save`：只把当前用户配置中的 map 部分写入 map worktree 的 `.gns/map/<有效 map-root>.toml`，不执行 `git add`、commit 或 push，也不修改 `map_root`、`git_root` 等配置值；这些值只能通过对应的显式配置命令修改；worktree 尚未初始化时只保留用户配置，由 `init` 首次物化配置快照；
- `load`：从 git-root 当前 HEAD 中的 `.gns/map/<有效 map-root>.toml` 读取机器配置快照并导入用户配置，不依赖 worktree 已经创建；
- `load` 只允许在 worktree 尚未初始化时执行，只加载配置，不创建 worktree，也不覆盖本机文件；加载后由首次 `gnm init` 应用全部映射；
- worktree 已初始化时执行 `load` 应报错并保持现状，后续映射调整使用会立即应用的 `gnm config add/remove`；
- 命令行没有提供 map-root 且用户配置中也没有 `map_root` 时，命令应报错并推荐先执行 `gnm config map-root <name>`；
- `add`、`remove` 或 `load` 改变有效映射后，删除当前机器的 `.syncable`，要求重新人工确认。

`save` 产生的配置文件修改与普通 worktree 修改使用同一套提交机制：

~~~text
手动提交：gnm config save → gnm add -A → gnm commit → gnm push
自动提交：gnm config save → 下一次 gnm sync 自动 add -A、commit、merge 和 push
~~~

### 3.5 增删映射的生效时机

worktree 尚未初始化时：

- `gnm config add` 只向用户配置写入映射定义；
- `gnm config remove` 只从用户配置删除映射定义；
- `gnm config remove -A/--all` 删除全部映射定义；
- 两条命令都不创建、移动、复制或删除本机文件。

worktree 已初始化后，`gnm config add` 在写入配置后，立即只对新增映射执行与 `gnm init` 相同的文件处理：

| worktree 内容 | 本机内容 | link 模式 | copy 模式 |
|---|---|---|---|
| 存在 | 不存在 | 在本机创建指向 worktree 的软链接 | worktree 增量复制到本机 |
| 不存在 | 存在 | 将本机内容移入 worktree，再以软链接替代本机路径 | 本机增量复制到 worktree |
| 都不存在 | 都不存在 | 不创建内容 | 不创建内容 |
| 都存在 | 都存在 | 以本机内容更新 worktree，再以软链接替代本机路径 | 以本机内容更新 worktree |

worktree 已初始化后，`gnm config remove` 执行单项 init 的反向操作，确保解除映射后本机仍保留可直接使用的真实文件。仓库内容是否删除由 scope 决定：

- `scope = "map-root"`：解除本机映射，并删除 worktree 中当前机器命名空间下的映射内容；
- `scope = "git-root"`：解除本机映射，但保留 worktree 中的公共内容，避免影响其他机器；
- link 模式解除映射时，先移除本机软链接，再把 worktree 内容物化为本机真实文件；`map-root` scope 可直接移回，`git-root` scope 使用复制以保留公共内容；
- copy 模式保留本机现有内容；本机内容不存在而 worktree 内容存在时，先复制回本机；只有 `map-root` scope 会继续删除 worktree 内容；
- 两侧都不存在时只删除配置定义；
- 完成后产生的 worktree 新增、修改或删除作为普通 Git 修改，等待用户手动确认并提交。

`gnm config remove -A/--all` 按照相同规则逐项解除所有映射。只有映射列表已经为空，才允许修改 `git-root`、`map-root` 或映射模式。

初始化后的 `add/remove` 都会删除 `.syncable`。操作完成后由 `gnm status` 展示结果，用户确认并成功执行 `gnm push` 后才恢复自动同步。

## 四、运行状态

map 只维护三个核心状态：

| 状态 | 条件 | 允许的操作 |
|---|---|---|
| `NOT_INITIALIZED` | worktree 尚未创建 | 配置、`map init` |
| `MANUAL_REQUIRED` | worktree 已创建，但不存在 `.syncable` | status、pull、add、get、commit、手动 push |
| `SYNCABLE` | 存在 `.syncable` | 手动操作和定时 `map sync` |

状态转换：

~~~text
NOT_INITIALIZED
        ↓ map init
MANUAL_REQUIRED
        ↓ 人工确认 + map commit + map push 成功
SYNCABLE
        ↓ 自动同步成功
SYNCABLE
        ↓ worktree merge git-root 冲突 / 无法 fast-forward / 映射根需要选择 / 有效映射变化
MANUAL_REQUIRED
~~~

运行时只需要保存必要的 worktree 信息、锁和 `.syncable`，不维护复杂的多端同步数据库。

### 4.1 `.syncable`

推荐位置：

~~~text
<gns-app-data>/map/<map-root>/.syncable
~~~

它是当前机器是否允许自动 commit 和 push 的安全闸门，不进入 Git 仓库。

创建条件：

- 首次人工 `map push` 完整成功；
- 阻断恢复后再次人工 `map push` 完整成功。

删除条件：

- worktree 分支合并 git-root 时发生冲突；
- git-root 无法 fast-forward 到 worktree HEAD；
- `git-root pull --ff-only` 确认本地与远程历史已分叉；
- 映射根仅在本机或 worktree HEAD 一侧存在，需要用户显式选择；
- 有效映射配置发生变化；
- 其他已经确认无法在不选择内容或历史的前提下继续的异常。

`.syncable` 表示“当前内容选择和 Git 历史仍被信任”，不表示每次运行都必须成功。断网、DNS、超时、认证或权限失败、map 锁竞争、Git 正在 merge/rebase/cherry-pick，以及 push 被拒绝，都只终止本次任务并保留 `.syncable`。它们不足以证明内容或历史已经需要人工选择，下一次同步仍从 `git-root pull --ff-only` 开始重试；只有届时确认无法 fast-forward，才删除标记并进入 `MANUAL_REQUIRED`。

## 五、初始化流程

### 5.1 首次配置

~~~text
1. 用户手动 clone 或创建 map Git 仓库
2. gnm config git-root <path>
3. gnm config map-root <name>
4. gnm config add ...
5. gnm init
6. gnm config save
7. 用户通过 gnm status 审查首次结果
8. gnm add <path...> 或 gnm get <path...>
9. gnm commit -m "initialize map"
10. gnm push
~~~

### 5.2 `gnm init`

`init` 只负责首次创建整台机器的 worktree 和现有映射，不用于应用后续配置变化。初始化完成后，新增或删除映射直接由 `gnm config add/remove` 执行单项正向或反向初始化，不需要再次运行 `gnm init`。

`init` 执行：

1. 校验 git-root、当前 HEAD、map-root 和映射配置；
2. 从 git-root 当前 HEAD 创建本地 `gns/map/<map-root>-worktree` 分支；
3. 在 GNS 应用目录创建 worktree；
4. 建立软链接或执行首次 copy；
5. 输出初始化结果；
6. 保持 `MANUAL_REQUIRED`，不创建 `.syncable`。

worktree 已经完整初始化时再次执行 `gnm init`，只报告“已经初始化”并正常结束，不修改任何文件。仅存在同名分支、残留目录或不完整 worktree 时视为初始化异常，报告具体路径并停止，不自动复用或修复。

首次文件规则：

| worktree 文件 | 本机文件 | 动作 |
|---|---|---|
| 存在 | 不存在 | 下发到本机 |
| 不存在 | 存在 | 本机内容写入 worktree |
| 都不存在 | 都不存在 | 不创建任何内容，保留映射配置 |
| 都存在 | 都存在 | 本机内容作为当前真实工作内容写入 worktree |

两侧都不存在表示当前内容一致，`status` 只展示该事实，不要求创建空文件，也不阻止首次手动 `push`。以后任意一侧出现内容时，再按映射根仅一侧存在的规则要求用户执行 `gnm add` 或 `gnm get`。

初始化不会把差异称为自动可解决的 Git 冲突。本机现有内容始终得到保留，但用户必须通过 `gnm status` 审查，并完成第一次手动 `gnm push` 后才能开启自动同步。

### 5.3 link 模式的初始化

link 模式始终先从 git-root 当前 HEAD 创建 worktree，再处理本机文件。初始化不会移动 worktree HEAD，也不会执行 `git add` 或 commit：

~~~text
worktree HEAD       = git-root 初始化时的版本
worktree 工作文件   = 本机原始版本（本机存在时）
本机路径            = 指向 worktree 工作文件的软链接
git diff            = 本机原始版本相对于 git-root 版本的差异
~~~

单项处理顺序：

1. 本机文件或目录存在时，用本机内容覆盖 worktree 对应工作文件；目录按本机内容完整同步；
2. 确认 worktree 内容写入成功后，将本机原路径临时改名保留；
3. 在本机原路径创建指向 worktree 工作文件的软链接；
4. 确认链接可访问且内容正确后，删除临时保留的本机副本；
5. 本机路径不存在但 worktree 内容存在时，直接创建软链接；
6. 两侧都不存在时不创建任何内容。

如果覆盖、改名或创建软链接任一步失败，应恢复本机原路径和操作前的 worktree 文件，并报告错误；不能留下丢失的本机文件、失效软链接或只完成一半的映射。

## 六、命令设计

### 6.1 短命令 `gnm`

`gnm` 是 map 功能的短命令入口：

~~~text
gnm status       = gns map status
gnm add -A       = gns map add -A
gnm sync         = gns map sync
gnm config       = gns map-config
gnm config add … = gns map-config add …
~~~

除 `config` 外，`gnm <command>` 等价于 `gns map <command>`；`gnm config <command>` 等价于 `gns map-config <command>`。长命令继续保留，本文优先使用较短的 `gnm`。

`gnm` 应由安装产物直接提供，并在程序入口完成上述命令展开，不依赖用户自行配置 shell alias。这样 Bash、PowerShell、Windows 任务计划和 daemon 都使用同一套命令行为。

### 6.2 `gnm status`

展示：

~~~text
map-root
git-root path / branch / HEAD
worktree path / branch / HEAD
worktree 是否 dirty
是否发生 merge 阻断
.syncable 是否存在
每个映射是否存在、类型是否匹配
~~~

`status` 只使用第四节定义的三个核心状态，其他信息作为原因和详情展示，不再扩展新的状态枚举：

| 状态 | 含义 |
|---|---|
| `NOT_INITIALIZED` | 尚未创建 worktree |
| `MANUAL_REQUIRED` | 等待首次确认、映射根选择或 Git 冲突处理 |
| `SYNCABLE` | 已通过人工确认，允许执行自动同步 |

`CLEAN`、`DIRTY`、首次初始化、映射根仅一侧存在和 merge conflict 都是状态详情或 `MANUAL_REQUIRED` 的原因，不是独立状态。网络、认证、权限、锁竞争和正在进行的 Git 操作也不增加新的状态枚举；它们只会暂停本次运行，不会单独导致 `.syncable` 被删除。

任何状态下，`status` 都必须给出可直接执行的下一步命令，而不只展示状态。主要组合的建议如下：

~~~text
NOT_INITIALIZED
  Next: gnm init

MANUAL_REQUIRED
  Reason: first initialization
  Next: gnm add <path>        # 保留本机版本
     or gnm get <path>        # 采用当前 HEAD 版本
  Then: gnm commit -m "..."
        gnm push

MANUAL_REQUIRED
  Reason: initial review complete, no changes
  Next: gnm push

MANUAL_REQUIRED
  Reason: mapping root differs
  Detail: path=~/.bashrc  local=missing  HEAD=exists
  Next: gnm add ~/.bashrc     # 确认删除
     or gnm get ~/.bashrc     # 从 HEAD 恢复
  Then: gnm commit -m "..."  # 仅 add 产生 staged changes 时需要
        gnm push

SYNCABLE + DIRTY
  Next: gnm sync              # 自动暂存、提交并同步
     or gnm add <path...>     # 人工检查后同步
        gnm commit -m "..."
        gnm push

SYNCABLE + CLEAN
  Next: 无需操作
  Optional: gnm sync          # 立即检查远程更新

MANUAL_REQUIRED
  Reason: merge conflict
  Next: gnm pull
        gnm status
        gnm add <path> 或 gnm get <path>
        gnm commit -m "resolve map conflict"
        gnm push

MANUAL_REQUIRED
  Reason: git-root cannot fast-forward to upstream
  Next: gnm pull --force
        gnm status
        gnm add <path> 或 gnm get <path>
        gnm commit -m "resolve map divergence"
        gnm push
~~~

merge conflict 必须列出发生冲突的具体本机文件路径，并直接生成可执行的 `gnm add/get <path>` 建议。操作性错误应根据具体原因给出针对性建议，例如重试原命令、等待锁释放、修复 Git 凭据或运行 `gnm config validate`。

### 6.3 `gnm add`

选择并暂存本机版本：

~~~text
gnm add <path...>     # 指定文件或目录：本机 → worktree → index
gnm add "~/.pi/*"    # 使用通配符选择多个文件或目录
gnm add -A            # 所有映射选择本机版本，并暂存整个 worktree
gnm add --all         # -A 的长参数形式
~~~

`add` 的参数是具体本机文件、目录或包含 `*` 的路径模式。相对路径按当前工作目录解析，也可使用绝对路径或 `~/`。目录参数递归处理其内容。

通配符由 GNS 跨平台展开。`*` 匹配单个路径层级内的任意名称，包括以 `.` 开头的名称，但不跨越路径分隔符；匹配到目录后递归处理整个目录。匹配范围只限已配置映射的本机路径及其子路径，不会处理未映射文件。`add` 使用本机与 worktree 文件的并集进行匹配，因此本机已经删除、但 worktree 中仍存在的文件也能被模式选中并暂存删除。模式没有匹配到任何映射内容时应报错，不静默成功。命令示例推荐给模式加引号，避免不同 shell 提前展开产生不一致行为。

软链接模式主要对对应 worktree 路径执行 `git add`；copy 模式先把指定本机内容增量复制到 worktree，再暂存对应路径。路径在本机已经不存在时，`add` 表示确认并暂存对应 worktree 内容的删除。`-A/--all` 对所有映射选择本机版本，最后对整个 worktree 执行 `git add -A`，因此也会暂存 `gnm config save` 写入的 `.gns/map/*.toml` 配置快照。

### 6.4 `gnm get`

选择 worktree 当前 HEAD 中的版本并下发本机：

~~~text
gnm get <path...>     # 指定文件或目录：HEAD → index + worktree 文件 → 本机
gnm get "~/.pi/*"    # 使用通配符选择多个文件或目录
gnm get -A            # 所有映射选择 HEAD 版本
gnm get --all         # -A 的长参数形式
~~~

`get` 使用与 `add` 相同的路径和通配符规则，并使用本机与 HEAD 文件的并集进行匹配。它将 HEAD 版本同时写入 index 和 worktree 文件，因此也会清除该路径之前已暂存或未暂存的差异。软链接模式下本机文件随 worktree 文件同步恢复；copy 模式恢复 worktree 文件后再复制到本机。操作完成后，该路径的 index、worktree 和本机文件都与 HEAD 一致，不产生需要提交的修改。

copy 模式不使用 watcher，而是在 `gnm add`、`gnm get` 和定时 `gnm sync` 中执行增量扫描。扫描前先检查每个映射根：

- 无论映射类型是单文件还是目录，只要映射根仅在本机或 worktree HEAD 一侧存在，`gnm sync` 都不自动判断这是新增还是删除，而是删除 `.syncable` 并进入 `MANUAL_REQUIRED`；
- 映射根在两侧都存在但文件类型不同（文件、目录或软链接）时，同样删除 `.syncable` 并进入 `MANUAL_REQUIRED`，由用户通过 `gnm add/get <path>` 选择最终类型；
- `gnm add <path>` 显式采用本机状态：本机存在时复制到 worktree，本机不存在时确认并暂存 worktree 删除；
- `gnm get <path>` 显式采用 HEAD 状态：HEAD 中存在时下发到本机，HEAD 中不存在时确认删除本机内容；
- 两侧映射根都不存在表示状态一致，不需要重复确认。

映射根检查通过，或者用户已经通过 `add/get` 明确方向后，文件过滤规则为：

1. 目标不存在或文件类型不同：执行复制或替换；
2. 文件大小不同：执行复制；
3. 文件大小相同但 mtime 不同：执行复制；
4. 文件大小和 mtime 都相同：跳过，不读取文件内容，也不计算 hash。

目录始终递归扫描，不使用目录自身的大小或 mtime 判断其子内容是否变化。映射目录本身仍存在时，源端已经删除的子文件属于普通增量变化，自动删除目标端对应子文件。复制采用临时文件和原子替换，成功后同步源文件的 mtime，保证下一次扫描能够直接跳过未变化文件。

copy 对文件类型和权限的处理规则：

- 普通文件复制内容、mtime 和文件权限；Linux/macOS 至少必须保留 executable bit，Windows 忽略文件系统不支持的 Unix 权限位；
- 映射目录内部的软链接按软链接本身复制，不跟随链接目标，避免越界复制和目录循环；比较软链接时比较其目标路径；
- 当前平台无法创建对应软链接时报告错误并终止当前命令，不静默改为复制链接目标；
- socket、FIFO、设备文件等特殊文件不复制，报告具体路径并终止当前命令；自动同步发生此类错误时删除 `.syncable`，进入 `MANUAL_REQUIRED`。

### 6.5 `gnm commit`

~~~text
gnm commit [-m <message>]
gnm commit [--message <message>]
~~~

执行：

1. 只提交已经由 `gnm add` 暂存或用户手动暂存的内容；
2. 有 staged changes 时创建 commit，无变更时正常结束；
3. 不隐式执行 `add -A`；
4. 不拉取、不合并、不推送，也不创建 `.syncable`；
5. 未指定 message 时生成默认提交信息，显式空 message 视为参数错误。

### 6.6 `gnm push`

`push` 是人工确认后的同步入口，不要求 `.syncable`。在 `MANUAL_REQUIRED` 状态下，它要求用户先通过 `add/get/commit` 完成选择，不会替用户自动接受尚未确认的文件版本。

完整成功后创建或恢复 `.syncable`。

### 6.7 `gnm sync`

`sync` 是 map 的自动同步入口：

~~~text
gnm sync
~~~

它必须在 `.syncable` 存在时运行，内部自动执行 `git-root pull → 映射根预检 → worktree add -A → commit → worktree merge git-root → 合并后映射根复检 → git-root fast-forward 到 worktree HEAD → git-root push`。映射根需要人工选择或没有 `.syncable` 时，只输出推荐的处理命令，不继续修改文件或 Git 状态。

### 6.8 `gnm pull`

`pull` 不是普通的无条件下发命令，而是发生阻断后进入人工恢复流程的入口。`gnm pull` 要求 git-root 能够 fast-forward；`gnm pull -f/--force` 则丢弃 git-root 与远程分支的历史分叉，强制以远程分支更新 git-root。两者都会把 worktree HEAD 和 index 更新到 git-root 新基准，但保证 worktree 文件和本机真实文件不变。

## 七、正常同步流程

### 7.1 前置条件

- git-root 是专用集成仓库，没有用户直接维护的未提交修改；
- git-root 当前 HEAD 必须位于配置了 upstream 的普通本地分支，不允许 detached HEAD；
- git-root pull 固定使用 fast-forward-only，不执行 rebase 或额外内容合并；
- worktree 分支只对应当前 map-root；
- 同一 map-root 的手动命令、定时任务和 daemon 使用同一把锁；
- 自动入口 `gnm sync` 必须存在 `.syncable`；
- 人工入口 `gnm push` 不要求 `.syncable`，但不会隐式暂存或提交文件。

### 7.2 执行顺序

`gnm push` 和 `gnm sync` 共用同一条集成链路，区别只在进入链路前如何处理本机修改：

- `gnm push`：要求用户已经通过 `gnm add/get/commit` 完成选择；如果仍有未提交或未暂存修改，则停止并由 `status` 给出建议；
- `gnm sync`：确认 `.syncable` 存在后，自动执行 `gnm add -A` 并创建默认提交，然后进入集成链路。

~~~text
1. 获取 map 锁
2. git-root pull --ff-only，与远程保持一致；确认无法 fast-forward 时删除 .syncable，其他异常保留 .syncable，均停止本次运行
3. 检查本机与 worktree 的映射根；仅一侧存在时删除 .syncable，进入 MANUAL_REQUIRED
4. sync 模式：确保本机文件与 worktree 文件一致，自动 add -A + commit
   push 模式：确认 worktree 已经由用户完成选择和提交
5. worktree merge git-root 当前 HEAD
6. 若冲突：恢复合并前的 worktree 文件，删除 .syncable，停止
7. 合并成功后再次检查映射根；仅一侧存在时恢复合并前状态，删除 .syncable，进入 MANUAL_REQUIRED
8. 把合并后的 worktree 内容下发到本机
9. git-root fast-forward 到 worktree HEAD；失败则报错、删除 .syncable 并停止
10. git-root push；失败时保留 .syncable 并停止，由下一次 pull --ff-only 判定是否已发生历史分叉
11. 人工 push 成功则创建 .syncable；sync 成功则保留 .syncable
~~~

对应关系：

~~~text
remote → git-root → worktree HEAD → worktree 文件 → 本机文件
                         ↑
                    本机修改 commit
~~~

### 7.3 唯一 Git 内容冲突点

只有第 5 步允许进入 Git 内容冲突状态：

~~~text
worktree merge git-root 当前 HEAD
~~~

映射根仅在一侧存在属于删除安全检查，不是 Git 冲突；它同样要求用户显式选择，但不会产生 conflict markers。

发生 Git 冲突时：

- 不继续更新 git-root；
- 不执行 git-root push；
- 中止本次 merge，并把 worktree 文件恢复到合并前已提交的本机内容；
- 删除 `.syncable`；
- 记录冲突路径和 git-root HEAD，供 status 与人工恢复使用；
- 由 `gnm status` 输出冲突文件和完整恢复步骤，等待用户执行 `gnm pull`。

git-root pull/push 的异常不进入内容冲突状态，也不会让 GNS 自动选择文件版本。默认只终止本次运行、保留 `.syncable` 并等待下次重试；只有 `pull --ff-only` 明确发现历史分叉，或后续集成步骤确认无法 fast-forward，才删除 `.syncable`。`gnm status` 在两种情况下都必须展示原因和推荐操作。

### 7.4 定时同步

不为 map 创建独立的 cron、系统定时任务或 daemon。用户只需启用：

~~~text
gns config set map.sync true
~~~

配置默认为 `false`。启用后，现有 GNS daemon 的每轮调度，以及 cron、systemd timer、launchd 或 Windows 任务计划触发的现有同步入口，都会额外执行 `gns map sync`。`gnm sync` 仍可用于手动立即执行一轮。

没有 `.syncable` 时直接输出 `MANUAL_REQUIRED` 和推荐的人工处理命令并退出，不执行自动 add、commit、merge 或 push。

## 八、阻断恢复流程

### 8.1 `gnm pull`

阻断后手动执行：

~~~text
1. 获取 map 锁
2. 记录 git-root 和 worktree 旧 HEAD
3. gnm pull：git-root pull --ff-only
   gnm pull -f/--force：git fetch 后将 git-root reset --hard 到当前分支的 upstream
4. worktree reset --mixed 到 git-root 当前 HEAD
5. worktree HEAD 和 index 指向新基准，worktree 文件和本机真实文件保持不变
6. 展示真实文件相对于新 HEAD 的差异
7. 用户按文件选择保留本机内容或 git-root HEAD 内容
8. gnm commit -m "resolve map conflict"
9. gnm push
~~~

`--force` 只强制覆盖不承载独立修改的 git-root，不对 worktree 使用 `reset --hard`。git-root 原有的本地提交会从分支尖端移除，但其对应的文件内容仍保留在 worktree 和本机真实文件中，随后作为相对远程新 HEAD 的未暂存差异由用户重新选择、提交和推送。

关键命令：

~~~powershell
git -C <worktree-path> reset --mixed <git-root-head>
~~~

执行结果：

~~~text
worktree 分支名       不变
worktree HEAD         指向 git-root 当前 HEAD
index                 重置为新 HEAD
worktree 文件         不变
本机真实文件          不变
~~~

禁止对 worktree 使用 `reset --hard`，因为它会覆盖正在使用的真实文件。`gnm pull --force` 只对不承载独立文件修改的 git-root 使用 `reset --hard`。

执行 `reset --mixed` 前应记录旧 HEAD，最好创建备份 ref，避免机器分支原有提交失去可恢复引用。

### 8.2 人工选择

用户可以按文件混合选择：

- **保留本机内容**：执行 `gnm add <path>`，将本机内容写入 worktree 并暂存；
- **采用 git-root 内容**：执行 `gnm get <path>`，从当前 HEAD 恢复并下发到本机；
- **手工合并**：编辑真实文件形成最终内容，再执行 `gnm add <path>` 暂存。

命令草案：

~~~text
gnm status
gnm add <path...>     # 保留本机版本
gnm get <path...>     # 采用当前 HEAD 版本
gnm commit -m "resolve map conflict"
gnm push
~~~

同一次恢复中可以对不同文件或目录分别使用 `add`、`get` 或手工合并。每次操作后，`gnm status` 都应展示剩余未处理路径和下一步命令。

整个处理期间 `.syncable` 始终不存在。只有最后一次手动 `gnm push` 完整成功，才重新创建它。

## 九、安全与可靠性

1. **真实文件不被隐式覆盖**：初始化、合并冲突和阻断恢复均保留本机已有内容。
2. **唯一内容冲突点**：只在 worktree merge git-root 时进入人工阻断。
3. **自动同步有闸门**：没有 `.syncable` 就不能执行 `gnm sync`，也不会自动 commit 或 push。
4. **worktree 禁止 hard reset**：切换 worktree 基准只使用保留文件的 `reset --mixed`；`--force` 只能对专用 git-root 使用 `reset --hard`。
5. **旧 HEAD 可恢复**：移动机器分支前记录旧 commit 或创建备份 ref。
6. **git-root 保持专用和干净**：机器修改只能通过 worktree 分支进入。
7. **完整流程加锁**：从 pull 到 push 持有同一 map 锁，避免本机并发操作。
8. **状态按机器隔离**：worktree、锁和 `.syncable` 均按 map-root 存放。
9. **异常不伪装成冲突**：网络、认证、权限、锁竞争、正在进行的 Git 操作和 push 拒绝都只终止当次运行并保留 `.syncable`；只有确认内容需要选择或历史无法 fast-forward 时才删除。
10. **copy 不做 watcher**：命令执行时增量扫描并复制，行为可预测。

## 十、命令总览（草案）

~~~text
gnm config git-root <path>
gnm config map-root <name>
gnm config add -a <map-path> <local-path>
gnm config add -A <repo-path> <local-path>
gnm config remove <local-path...>
gnm config remove -A | --all
gnm config list
gnm config validate
gnm config save [<map-root>]
gnm config load [<map-root>]

gnm init
gnm status
gnm add <path-or-pattern...> | -A | --all
gnm get <path-or-pattern...> | -A | --all
gnm commit [-m <message>]
gnm pull [-f | --force]
gnm push
gnm sync
~~~

命令名称仍可调整，但职责边界保持不变：

~~~text
gnm config = 定义和迁移映射配置（完整命令：gns map-config）
gnm        = 操作本机文件、worktree 和 git-root 的状态流转（完整命令：gns map）
~~~

## 十一、待定事项

- `.syncable` 使用空标记文件还是保存版本和最后成功时间；
- 操作中断后如何记录执行阶段和旧 git-root/worktree HEAD，以及使用隐藏 ref、备份分支还是状态文件；
- `gnm config load <map-root>` 是否支持在导入时复制为新的 map-root；
- 是否允许单条映射覆盖全局 `auto/link/copy` 模式；
- copy 是否增加可选的 hash 校验；
- 是否增加 `dry-run`、批量迁移和卸载等辅助能力；
- 自动 commit message 的默认格式；
- 文档定稿时将 `.syncable` 等重复规则收敛为单一定义，其他章节只保留引用。
