# git-notes-sync 使用说明

> 面向 Git 工作区的自动同步工具（Markdown / Obsidian 笔记首选场景，任意文本仓库通用）。
> 核心：一条命令完成 `提交 → fetch → merge → push`，冲突不阻断同步。

---

## 1. 安装

### 方式一：npm（推荐，无需 Go 环境）

postinstall 按平台从 GitHub Releases 下载对应二进制（`gns-<platform>-<arch>[.exe]`），自动跟随重定向、校验 SHA-256（Release 的 `checksums.txt`）、验证 `--version` 后落盘。安装后提供两个等价命令：

```bash
gns --version        # 或 notes-sync --version
```

**两种安装来源**：

```bash
# 方式一：npm registry（平台分包，零 flag；主包无 install 脚本不触发 allow-scripts）
npm install -g git-notes-sync

# 方式二：GitHub 直装（同架构；无需 --allow-scripts/--foreground-scripts，仅需 --install-links=true）
npm install -g --install-links=true github:aweyonhub/git-notes-sync
# 开发版（临时开发分支，如 <branch>）
npm install -g --install-links=true github:aweyonhub/git-notes-sync#<branch>
```

> 版本对应关系：包 `package.json` 的 version = git tag = GitHub Release = 下载器拉取的资产版本；`#<branch>` 分支若 package.json 版本未变，下载的仍是当前 Release 的二进制（测试安装链路 OK，测试新二进制需先发对应版本或本地构建）。

**npm 11+ 默认拦截 install 脚本**（allow-scripts 安全机制），首次安装需放行（不同 npm 小版本的提示不同，任选其一）：

```bash
# 方式一：安装时放行（推荐，配合 --install-links=true）
npm install -g --install-links=true --foreground-scripts --allow-scripts=git-notes-sync github:aweyonhub/git-notes-sync
# 方式二：永久放行（user 级配置）
npm config set allow-scripts=git-notes-sync --location=user
# 方式三：npm 11.2+ 的 approve 流程（按提示执行）
npm approve-scripts git-notes-sync
```

> **为什么必须 `--install-links=true`**（2026-08-14 实测）：npm 对 git 依赖（`github:` 语法）默认符号链接到 `cacache/tmp/git-cloneXXX` 临时目录，`npm list -g` 显示 `-> ...\git-cloneXXX`；临时目录被 npm 清理后包就失效（MODULE_NOT_FOUND 的根源）。`--install-links=true` 强制复制解包成普通目录；`--foreground-scripts` 前台执行脚本避免竞态。

**下载器环境变量**（企业代理/内网镜像/固定版本）：

| 变量 | 作用 |
|------|------|
| `HTTPS_PROXY` / `HTTP_PROXY` | 正向代理（CONNECT 隧道） |
| `GNS_VERSION` | 强制下载版本（默认取 package.json version） |
| `GNS_REPO` | 仓库 `owner/name`（默认 `aweyonhub/git-notes-sync`） |
| `GNS_RELEASE_BASE_URL` | Releases 基础地址（内网镜像） |
| `GNS_CHECKSUM_URL` | checksums.txt 地址（默认 `<base>/checksums.txt`；缺失时跳过校验） |
| `GNS_SKIP_INSTALL=1` | 跳过下载（二进制已存在时） |

### 方式二：手动构建（需要 Go 1.22+）

```bash
make build            # 生成 ./gns
make cross            # 交叉编译全部 5 平台到 dist/
```

### 前置要求

- **系统 Git**（`git --version` 可运行）——本工具不内置 Git，全部调用系统 Git
- 仓库已配置远端与上游：`git remote add origin <url>` + `git push -u origin main`

**笔记仓库是 GitHub 私有仓库时（常见场景）**：认证完全由系统 git 处理，本工具不介入。推荐 PAT + credential helper 持久化（一次配置，终端/daemon/cron 全部可用）：

```bash
git config --global credential.helper store     # 凭据存入 ~/.git-credentials（明文，仅本机）
git push                                        # 首次输入用户名 + PAT 作为密码，之后免输入
# 或（token 不进本地文件，但需终端可用）：
git remote set-url origin https://<PAT>@github.com/you/notes.git
```

> ⚠️ daemon / cron 运行时需保证 `HOME` 指向含凭据的目录（见 §5 环境注意事项）；PAT 建议只授予 `repo` 权限、定期轮换。

---

## 2. 快速上手

```bash
cd ~/notes                          # 进入笔记仓库

gns status                        # ① 查看仓库状态（分支/落后领先/冲突）
gns sync                          # ② 手动同步一次：commit → fetch → merge → push

# ③（推荐）多仓库场景：注册到全局配置，之后用名字同步
gns repos add ~/notes -name notes  # 写入 ~/.config/git-notes-sync/config.toml
gns sync-all                       # 同步全部仓库 / gns sync notes 同步单个

# ④（可选）定时任务，见「5. 定时调度」；单仓库零配置也可直接用默认值
```

首次运行建议先在终端手动执行 `gns sync`，确认输出正常后再配置定时任务。

---

## 3. 命令详解

### `gns sync` — 核心同步命令

执行完整同步流程：

```
可选自动提交（受 debounce / max_wait 控制）
  ↓
保护未提交工作区（绝不覆盖用户修改）
  ↓
fetch（网络失败自动重试 3 次，指数退避）
  ↓
merge 远端（非 rebase，保留双向历史）
  ↓
文本冲突 → 保留 markers → merge commit（不阻断）
  ↓
push（远端有更新时自动重新 fetch + merge，最多 3 轮）
```

```bash
gns sync                # 同步当前目录仓库（默认）
gns sync notes          # 按配置中的 repo 名字同步
gns sync ~/notes        # 或直接给路径
gns sync -p ~/notes     # -p/-repo 指定路径（等价）
gns sync -c my.toml     # 指定配置文件
gns sync-all            # 同步配置中所有 repos（等价 daemon 一轮）
```

> 位置参数解析规则：先匹配配置 repos 列表中的 name/path（支持 `~/` 展开），未命中则当作路径。`status` / `commit` / `resolve` 同样支持 `gns status notes` 这种写法。

### `gns commit` — 立即提交

```bash
gns commit              # 忽略 debounce，立即提交当前所有修改
gns commit -message "自定义信息"
gns commit -force       # 显式强制（默认 commit 即忽略时机）
```

### `gns commit-ai` — AI 提交

```bash
gns commit-ai           # AI 根据 diff 生成提交信息；AI 不可用时自动降级
```

### `gns status` — 状态查看

```bash
gns status
```

输出示例：

```
repo: /home/me/notes
branch: main (tracking origin/main)
remote: ahead 2 | behind 1 (vs origin/main)
worktree: 3 change(s)
  M docroot/10-note/mac/aerospace.md
  ?? scratch/idea.md
conflicts: 1 file(s)
  docroot/20-collect/draft.md (2 block(s))
```

### `gns resolve` — 处理持久化的冲突

```bash
gns resolve                      # ① 列出含冲突 markers 的文件（默认）
gns resolve --ours               # ② 全部保留本地版本，去 markers，提交并推送
gns resolve --theirs             # ③ 全部保留远端版本，去 markers，提交并推送
gns resolve --ai                 # ④ AI 逐文件语义合并（需配置 [ai]）
```

> `--ours` / `--theirs` 是一次性处理**所有**冲突文件；AI 失败的单个文件会保留 markers 并跳过，不丢数据。

### `gns repos` — 多仓库管理

维护全局配置中的 repos 列表（写入 `[[repos]]` 表，保留文件其他内容和注释）：

```bash
gns repos list                    # 列出 name + path
gns repos add ~/notes -name notes # 添加（-name 省略时用目录名）
gns repos del notes               # 删除（按 name 或 path）
gns repos add ~/wiki -c my.toml   # 指定配置文件（默认全局配置）
```

### `gns config` — 查看与编辑配置

查看有效值（合并全局 + 仓库级配置后的结果）或编辑全局配置中的标量字段。写入采用行级编辑，保留注释与其他字段。

```bash
gns config list                    # 列出所有字段的有效值（覆盖默认时标 [default: X]）
gns config get sync_interval       # 取单个值（嵌套用点号：ai.timeout）
gns config set sync_interval 600   # 写入值（bool/int/string 自动类型推断）
gns config set ai.timeout 90       # 写入嵌套表字段
gns config set commit_message ai   # 字符串自动加 TOML 引号
gns config unset sync_interval     # 删除 key，回落到默认
gns config list -c my.toml         # 指定配置文件（默认全局配置）
```

说明：
- key 用点号访问嵌套表，如 `ai.timeout`、`conflict.strategy`。
- `list`/`get` 显示合并后的**有效值**；配置文件不存在时显示默认值。
- `repos`（块）与 `conflict.text_extensions`（数组）不可用 `config set` 编辑——前者用 `gns repos add/del`，后者手编配置文件。
- `set` 只做类型校验（bool/int/string）；业务约束（如 `sync_interval` 最小 5）在加载时生效。
- daemon 会检测配置文件 mtime 变更自动热重载，`set` 后下一轮即生效。

### `gns daemon` — 轻量常驻（Windows 首选）

```bash
gns daemon              # 按 sync_interval（默认 600s = 10min）定时同步所有 repos
gns daemon --once       # 只跑一轮（可用于测试）
gns daemon -c my.toml   # 指定全局配置
```

daemon 只做两件事：内部 timer 周期触发同步、缓存配置（配置变更自动热重载）。不做 watcher、无状态持久化。

### `gns version` / `gns help`

```bash
gns version             # 版本号
gns help                # 命令帮助
```

---

## 4. 配置参考

### 4.1 配置文件位置与合并规则

| 优先级 | 位置 | 说明 |
|--------|------|------|
| 低 | 内置默认值 | 见下表"默认"列 |
| 中 | 全局配置 | Linux/macOS：`~/.config/git-notes-sync/config.toml`；Windows：`%APPDATA%\git-notes-sync\config.toml` |
| 高 | 仓库配置 | 仓库根目录 `.notes-sync.toml`（随笔记仓库走，可进 git 版本管理） |

规则：后加载的覆盖先加载的（仓库 > 全局 > 默认）。也可用 `-c 文件` 显式指定并置于仓库配置之前加载。

**推荐用法（本项目主推）：全局配置 + repos 列表**

- **所有仓库参数统一放全局配置** `~/.config/git-notes-sync/config.toml`（Windows：`%APPDATA%\git-notes-sync\config.toml`），用 `gns repos add` 维护仓库名单，**无需在每个项目中放置配置文件**：
  ```bash
  gns repos add ~/notes -name notes     # 写入全局配置
  gns sync notes / gns sync-all / gns daemon   # 按名字同步/全量同步/定时同步
  ```
- **仓库级 `.notes-sync.toml` 是可选的覆盖手段**：一般没必要使用；仅当某个仓库需要与全局不同的设置（如更短的 debounce、不同的提交信息模式）时，在该仓库根放一个即可，它会覆盖全局配置的对应项（配置跟随仓库走，可进 git 版本管理）。
- 单仓库用户也可以完全零配置——内置默认值（`auto_commit`、debounce 60s、max_wait 300s、timestamp 提交信息）开箱即用。

### 4.2 配置项全表

| 配置项 | 默认 | 说明 |
|--------|------|------|
| `auto_commit` | `true` | 同步前是否自动提交工作区修改 |
| `commit_debounce` | `60` | 最近一次修改距今不足 N 秒则推迟提交（避免打断编辑） |
| `commit_max_wait` | `300` | 修改待处理超过 N 秒强制提交（防止长期未落盘） |
| `commit_message` | `"timestamp"` | `timestamp`（时间戳+diff 摘要）\| `static`（固定文本+摘要）\| `ai`（AI 生成） |
| `commit_static_message` | `"notes: auto sync"` | `static` 模式的固定文本 |
| `ai_fallback` | `"timestamp"` | AI 失败时的降级：`timestamp` \| `static` |
| `binary_strategy` | `"ours"` | 二进制冲突：`ours`（保留本地副本）\| `abort`（中止同步） |
| `sync_interval` | `600` | daemon 轮询间隔（秒，最小 5，默认 600 = 10 分钟） |
| `retry_attempts` | `3` | fetch/push 网络失败重试次数（2s/4s/8s 退避） |
| `repos` | `[]` | daemon 遍历的仓库列表；为空则当前目录 |
| `[conflict] strategy` | `"preserve"` | 文本冲突：`preserve`（保留 markers 继续同步）\| `abort`（中止） |
| `[conflict] text_extensions` | 见下 | 视为文本的扩展名列表，默认 `.md .markdown .txt`（需其他格式自行扩展） |
| `[ai] type` | 空 | `api`（OpenAI 兼容接口）\| `command`（任意 CLI） |
| `[ai] base_url` | 空 | API 地址，如 `https://api.openai.com/v1` |
| `[ai] model` | 空 | 模型名 |
| `[ai] api_key_env` | `"NOTES_AI_API_KEY"` | API Key 所在环境变量名 |
| `[ai] command` | 空 | command 模式的 CLI 命令 |
| `[ai] timeout` | `60` | AI 调用超时（秒） |
| `[ai] max_diff_bytes` | `51200` | 发送给 AI 的 diff 截断上限 |
| `[ai] agent_file` | `"AGENTS.md"` | 仓库级 agent 指令文件（相对仓库根），随 diff 一起发给 AI；文件不存在则忽略 |

### 4.3 完整示例

```toml
# ~/.config/git-notes-sync/config.toml（推荐，全局）
# 或 ./.notes-sync.toml（仓库级覆盖，一般不需要）

auto_commit = true
commit_debounce = 60
commit_max_wait = 300
commit_message = "timestamp"        # timestamp | static | ai
commit_static_message = "notes: auto sync"
ai_fallback = "timestamp"
binary_strategy = "ours"
sync_interval = 600
retry_attempts = 3

# 多仓库列表（daemon / gns sync-all / gns sync <name> 使用）
# 写法一：简单数组（显示名 = 路径）
# repos = ["~/notes", "~/work/wiki"]
# 写法二：命名表（可用 gns repos add/del 维护；推荐，便于按名字同步）
[[repos]]
name = "notes"
path = "~/notes"

[[repos]]
name = "wiki"
path = "~/work/wiki"

[conflict]
strategy = "preserve"
text_extensions = [".md", ".markdown", ".txt"]

[ai]
type = "api"
base_url = "https://api.example.com/v1"
model = "model-name"
api_key_env = "NOTES_AI_API_KEY"
agent_file = "AGENTS.md"    # 仓库级指令文件，随 diff 发给 AI（默认，不存在则忽略）
timeout = 60
max_diff_bytes = 51200
```

---

## 5. 定时调度

### Linux — cron

```bash
crontab -e
# 注意：cron 的 PATH 只有 /usr/bin:/bin，且环境变量极少。
# 方案一（最稳）：写 gns 的绝对路径（npm 全局 bin 一般 /usr/local/bin）
# 单仓库：每 5 分钟同步（cd 到仓库目录）
*/5 * * * * cd /home/me/notes && /usr/local/bin/gns sync >> /tmp/gns-sync.log 2>&1
# 多仓库：每 5 分钟同步配置中所有 repos（sync-all 无需 cd）
*/5 * * * * /usr/local/bin/gns sync-all >> /tmp/gns-sync.log 2>&1

# 方案二：先显式设置 PATH 再直接调命令（可含 SSH_AUTH_SOCK 等）
PATH=/usr/local/bin:/usr/bin:/bin:/home/me/.npm-global/bin
*/5 * * * * gns sync-all >> /tmp/gns-sync.log 2>&1
```

### macOS — launchd

创建 `~/Library/LaunchAgents/com.git-notes-sync.plist`，核心配置：

```xml
<key>StartInterval</key><integer>300</integer>
<key>ProgramArguments</key>
<array>
  <string>/usr/local/bin/gns</string>
  <!-- 单仓库：sync（配合 WorkingDirectory）；多仓库：sync-all（忽略目录） -->
  <string>sync-all</string>
</array>
<key>WorkingDirectory</key><string>/Users/me</string>
```

```bash
launchctl load ~/Library/LaunchAgents/com.git-notes-sync.plist
```

### Windows — daemon（首选）或任务计划程序

**方式一：daemon**

```bash
gns daemon
```

启动后每 600s（10 分钟）自动同步所有 `repos`。开机自启：将 `gns daemon` 放入「启动」文件夹或任务计划程序。异常退出后重启即恢复，无状态恢复逻辑。

**方式二：任务计划程序**

- 触发器：每 5 分钟 / 登录时
- 操作：`gns.exe sync-all`（同步配置中所有 repos，无需设工作目录）；单仓库可用 `gns.exe sync` 并设置工作目录
- ⚠️ 注意：任务计划程序的环境变量受限，SSH agent / credential helper 需保证可用

### daemon / cron 环境注意事项

终端里 `git push` 成功但定时任务失败，通常是环境变量问题。需确保：`SSH_AUTH_SOCK`（SSH 认证）、credential helper（HTTPS 凭据）、`PATH` / `HOME` 完整。

---

## 6. AI 集成（可选）

### 6.1 提交信息模式说明

`commit_message` 三种模式（示例见 §3 `gns commit` 章节）：

- **`timestamp`**（默认）：时间戳 + diff 摘要，如：

  ```
  notes: 2026-08-13 11:30

   files: 3 changed, +42, -8
   - docroot/10-note/mac/aerospace.md (+20, -3)
   - docroot/20-collect/draft.md (+7)
  ```

- **`static`**：固定文本（`commit_static_message`，默认 `"notes: auto sync"`）+ 同样的 diff 摘要，适合希望提交信息稳定可搜索的场景（如同步插件过滤）：

  ```
  notes: auto sync

   files: 3 changed, +42, -8
   - docroot/10-note/mac/aerospace.md (+20, -3)
   - docroot/20-collect/draft.md (+7)
  ```

> 三种模式的共同点是 **diff 摘要**：` files: N changed, +X, -Y` + 每文件行数增减（前 20 个文件，超出省略），来源 `git diff --cached --numstat`。
- **`ai`**：AI 根据 diff 生成语义化信息；失败自动降级到 `ai_fallback`

### 6.2 API 方式（OpenAI 兼容）

```toml
[ai]
type = "api"
base_url = "https://api.openai.com/v1"   # 或任意兼容服务 / 本地 Ollama
model = "gpt-4o-mini"
api_key_env = "NOTES_AI_API_KEY"          # export NOTES_AI_API_KEY=sk-...
```

### 6.3 Command 方式（任意 CLI）

```toml
[ai]
type = "command"
command = "codex exec --format openai ..."   # 或 ollama run qwen2.5 / 自定义程序
```

约定：**stdin = git diff（或待解决冲突文件内容），stdout = 提交信息（或解决结果）**。退出码非 0 或超时视为失败。

### 6.4 Agent 指令文件

`[ai] agent_file`（默认 `AGENTS.md`）指向仓库根的一个指令文件，内容会随 diff 一起作为 system prompt 的一部分发给 AI——适合固定提交风格、语言、排除规则等（例如要求提交信息用中文、标注 breaking change）。文件不存在时静默忽略，不影响调用。

### 6.5 降级机制（重要）

AI 是**增强而非依赖**：API 不可用、网络错误、quota 用尽、CLI 未安装、返回格式异常、超时——任何失败自动按 `ai_fallback` 降级（默认 timestamp 摘要），**同步链路不受任何影响**。

---

## 7. 冲突处理指南

### 7.1 冲突如何发生

两端同时修改同一文件的同一区域 → merge 产生冲突。本工具的处理哲学：**冲突不是同步失败，是可延迟解决的数据状态**。

### 7.2 冲突后发生了什么

1. 冲突文件保留双方内容与 markers（`<<<<<<<` / `=======` / `>>>>>>>`）
2. 自动 `git add` + merge commit + push —— **同步继续，不中断**
3. 冲突内容已提交到历史，随时可用 `gns resolve` 事后解决

### 7.3 解决流程

```bash
gns status          # 查看冲突文件
gns resolve         # 列出含 markers 的文件及块数
# 人工/Agent 编辑后：
gns commit           # 提交解决结果
# 或一键处理：
gns resolve --ours      # 全部保留本地版本
gns resolve --theirs    # 全部保留远端版本
gns resolve --ai        # AI 语义合并（建议人工复核）
```

### 7.4 二进制冲突

二进制文件无法保留 markers，按 `binary_strategy` 处理：

- `ours`（默认）：保留本地副本，继续同步（注意：远端版本被覆盖）
- `abort`：中止 merge 并提示，需人工处理

---

## 8. 常见问题（FAQ）

| 现象 | 原因与处理 |
|------|-----------|
| `merge origin/main failed: Your local changes ... would be overwritten` | `auto_commit=false` 且工作区有未提交修改与远端冲突。`gns commit` 或 stash 后重试 |
| `push rejected (fetch first)` | 远端在你 fetch 后又更新。工具已自动重 fetch + 重 merge 重试（≤3 轮）；若仍失败说明远端持续移动，手动处理 |
| `git is in MERGE_HEAD state` | 存在未完成的 merge/rebase（可能来自其他工具）。先手动完成或 `git merge --abort` |
| `another sync is running (lock: ...)` | 上一次同步未正常结束。锁 10 分钟自动过期；也可删除 `.git/git-notes-sync.lock` |
| AI 未生效 | 检查 `commit_message = "ai"`、`[ai] type` 已配置、`api_key_env` 指向的环境变量已导出 |
| `commit` 报 "Please tell me who you are" | 未配置 git 身份：`git config --global user.name/email` |
| 与 Obsidian Git 插件冲突？ | 不冲突。本工具在系统层操作 Git，不介入编辑器进程，可共存或互补 |
| daemon 里 push 失败但终端成功 | 环境变量问题（SSH agent / credential helper / PATH），见「5. 环境注意事项」 |
| 同步间隔多长合适 | 笔记场景 60s~10min 均可（默认 10min）；文件多/仓库大时可调大 `sync_interval` 或 cron 间隔 |

---

## 9. 与开发相关

- 规格文档：[git-notes-sync.md](../git-notes-sync.md)（含 §七 实现决策）
- 开发状态：[STATUS.md](./STATUS.md)
- 完整配置示例：[example.config.toml](../example.config.toml)
