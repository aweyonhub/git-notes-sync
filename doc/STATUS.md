# git-notes-sync 开发状态

> 更新：2026-08-14 · 基于 [git-notes-sync.md](../git-notes-sync.md) 规格开发（规格第七节已固化实现决策）

---

## 一、确认内容

### 1.1 文档开放问题（§六）→ 已给出方案

| # | 开放问题 | 采用方案 | 状态 |
|---|---------|---------|------|
| 1 | 是否内置纯 Go Git 实现 | 不做，调用系统 Git（规格 §1.3 非目标），`internal/git` 留替换扩展点 | ✅ 已落地 |
| 2 | 冲突批量语义解决的调度方式 | `gns resolve --ours/--theirs/--ai` 手动触发；daemon 不做自动语义解决；AI 失败保留 markers 不丢数据 | ✅ 已落地 |
| 3 | 定时任务间隔推荐值 | daemon `sync_interval=600s`（最小 5s）；cron 建议 `*/5 * * * *` | ✅ 已落地 |

### 1.2 开发中歧义 → 默认决策（共 19 项，详见 spec §七）

关键项摘录：

| 歧义 | 默认决策 |
|------|---------|
| 命令名 | 二进制 `gns`；npm bin 注册 `gns`（主）+ `notes-sync`（别名） |
| 配置文件位置 | 仓库 `.notes-sync.toml` + 全局配置（macOS `~/Library/Application Support/git-notes-sync/config.toml`、Linux `~/.config/git-notes-sync/config.toml`、Windows `%APPDATA%\git-notes-sync\config.toml`，可被 `GNS_CONFIG` 覆盖），仓库覆盖全局 |
| debounce/max_wait 计时 | 基于 `.git/git-notes-sync.state` 记录 first_seen，跨 cron 无状态运行仍可兜底 |
| 二进制冲突 | `binary_strategy = "ours"`（保留本地副本）默认 |
| static 模式固定文本 | `commit_static_message`（默认 `"notes: auto sync"`） |
| retry / 锁 | fetch/push 3 次指数退避；`.git` 内 lock 文件 10 分钟过期 |

### 1.3 待用户拍板（不阻塞开发，可随时改）

- [x] **GitHub 仓库**：https://github.com/aweyonhub/git-notes-sync（2026-08-13，占位符已替换）
- [x] **命令名**：已确认使用 `gns`（2026-08-13）；npm 同时提供 `notes-sync` 别名
- [ ] **国内网络**：是否追加 `GOPROXY=https://goproxy.cn,direct` 到 bashrc
- [ ] **Windows 本机**：是否安装 Go（WSL 已装 `~/go-sdk/go`，仅开发需要）

---

## 二、已完成内容

### 2.1 核心功能（对应规格 §4.1–4.7）

| 模块 | 内容 | 验证 |
|------|------|------|
| 同步引擎 | commit → fetch → merge(非 rebase) → push；merge 中保护未提交内容；push 被拒自动重 fetch+重 merge（≤3 轮）；retry 指数退避；lock 防并发；merge/rebase 状态检测 | ✅ 集成测试 |
| 冲突处理 | 文本冲突保留 markers → git add → merge commit → push；二进制按 `binary_strategy` 保留本地副本或中止；text/binary 按扩展名 + NUL 嗅探判定 | ✅ 集成测试 |
| 提交管理 | debounce 推迟 + max_wait 强制兜底 + state 持久化；timestamp/static 模式带 diff 摘要（文件列表+行数）；批量提交 | ✅ 集成测试 |
| AI 集成 | OpenAI-compatible API + 任意 CLI 双后端；stdin=diff（system/AGENTS.md 以 `### Instructions` 段包裹）/ stdout=message；diff 截断 50KB；任何故障 fallback（`ai_fallback`） | ✅ fallback 测试 |
| resolve | 双路检测冲突文件（`git grep` markers + `ls-files -u`）；`--ours/--theirs/--ai`；解决后 commit + push；CRLF 兼容 | ✅ 集成测试 |
| status | 仓库/分支/upstream/ahead-behind/工作区变更/冲突列表 | ✅ 冒烟 |
| daemon | 轻量 timer（默认 600s = 10min）；配置 mtime 变更热重载；多仓库 `repos` 遍历；`--once` 单次 | ✅ 冒烟 |
| config | `gns config list/get/set/unset` 标量配置编辑；行级编辑保留注释与 `[[repos]]` 块；类型推断（bool/int/string）；`list` 标注默认值覆盖 | ✅ 单测 |
| CLI | sync / commit / commit-ai / status / resolve / repos / config / daemon / version / help | ✅ 冒烟 |

### 2.2 测试

- **9 个集成测试**（真实 git 双仓库场景）：快进合并+push、文本冲突保留+resolve 回写远端、二进制冲突、debounce 三态、AI fallback、resolve AI 未配置、无 upstream、非仓库
- **5 个单测**：marker 解析（ours/theirs/多块/未闭合/CRLF）
- 全部通过；`go vet`、`gofmt` 干净

### 2.3 分发与工程化

- npm 分发（当前，双源独立）：**registry 源**=方案③平台分包——主包 `packages/meta/`（无 install 脚本，bin=bin/gns.js shim，optionalDependencies 锁 6 个平台子包）+ `packages/cli-<os>-<arch>`（os/cpu 字段 + 原生二进制），零 flag；**GitHub 直装源**=仓库根 package.json（方案① postinstall 下载器从 GitHub Releases 拉二进制，自包含不依赖 registry，三 flag：`--install-links=true --foreground-scripts --allow-scripts=git-notes-sync`）。发布流程 `make cross && scripts/assemble-platform-packages.sh <ver>`（单点同步根 + meta + 子包版本）→ 先 6 子包后主包（packages/meta）`npm publish --access public`；CI tag 触发全自动（publish job + NPM_TOKEN）
- npm 壳（历史，方案①）：仓库根即 npm 包（postinstall 按平台下载 Releases 二进制）；下载器保留在 `npm/scripts/install.js`（curl 直装 fallback）
- GitHub Actions：tag 触发交叉编译 6 平台 + 发布 Release
- Makefile：build / test / vet / cross / clean
- README.md + example.config.toml（全量配置注释）
- 规格更新：git-notes-sync.md 追加 §七 实现决策（19 项）

### 2.4 Windows 调度黑窗规避（wscript + .vbe）

任务计划**不能直接注册 `gns.exe`**：每次触发都会弹黑色控制台窗口（gns 是控制台程序）。方案：任务注册 `wscript.exe` 执行一个 `.vbe` 包装脚本，由它隐藏窗口拉 gns：

- **注册**：`schtasks /TR 'wscript.exe "%LOCALAPPDATA%\git-notes-sync\<label>.vbe"'`（wscript 是 GUI 程序，进程本身不弹窗）
- **.vbe 内容**：一行 VBS 脚本 `CreateObject("WScript.Shell").Run "<gns 命令>", 0, False`——`Run(..., 0)` 的窗口状态 0 = SW_HIDE，隐藏启动；`False` 不等待
- **gns 命令**：`"<exe>" sync-all --log "<path>"`（interval）或 `"<exe>" daemon -c "<cfg>" --log "<path>"`（daemon）；VBS 字符串字面量中双引号写为 `""` 转义（`taskVbeContent` 用 `strings.ReplaceAll` 处理）
- **日志**：`--log` 由 gns 内部重定向 + 轮转，与 macOS/linux 一致（三平台统一）
- **为什么不是 cmd /c**：早期方案 `cmd /c ""exe" sync-all >> log 2>&1"` 有引号剥层陷阱且 cmd 窗口闪烁；wscript 方案实测无闪烁、无引号错乱
- **测试**：`internal/service/windows_test.go` 覆盖 `taskVbeContent` 引号转义（含 exe/log 路径带引号场景）与 `taskCreateArgs`

### 2.5 开发环境

- Go 1.22.5 安装于 WSL `~/go-sdk/go`（无 root，解压即用）
- PATH 已持久化到 `~/.bashrc`（符号链接目标文件），新终端直接可用

---

## 三、待完成内容

| 优先级 | 事项 | 说明 |
|--------|------|------|
| P0 | ~~创建 GitHub 仓库并替换占位符~~ | ✅ 已完成：https://github.com/aweyonhub/git-notes-sync（module path 同步更新） |
| P0 | ~~打 tag 发布 v0.1.0~~ | ✅ 已完成（重新发布，旧版已删）：v0.1.0 CI 全绿，Release 6 资产（5 平台二进制 + `checksums.txt`）（https://github.com/aweyonhub/git-notes-sync/releases/tag/v0.1.0） |
| P0 | ~~验证 npm 安装链路~~ | ✅ **Windows 实测通过（2026-08-14）**：`npm install -g --install-links=true --foreground-scripts --allow-scripts=git-notes-sync github:aweyonhub/git-notes-sync#dev` 成功，postinstall 下载二进制，`gns --version` 正常；关键修复：files 白名单改目录形式、shim 补 shebang、**--install-links=true 强制复制解包**（git 依赖默认符号链接到 cacache 临时目录是失效根源）；CI 有 npm-install job 持续守护 |
| P1 | 真实 AI endpoint 冒烟 | 本地 Ollama 或任意 OpenAI-compatible 服务验证 `commit_message="ai"` 与 `resolve --ai` |
| P1 | Windows 实机验证 | ✅ daemon 行为、credential helper 继承已验；autocrlf 场景待续（v0.1.4 已含 wscript + .vbe 隐藏窗口方案，见下） |
| P2 | `gns resolve` 交互式模式 | 逐个文件选择 ours/theirs/AI（当前为全局 flag 一次性处理） |
| P2 | ~~cron 示例文档完善~~ | ✅ 已完成：USAGE §5 有 cron/launchd/Windows 三种示例 + launchd plist 完整模板 + `gns install` 一键注册 |
| P2 | `gns install` / `gns uninstall` 注册/管理系统服务 | ✅ **macOS 已落地（v0.1.4）**：launchd LaunchAgent（`~/Library/LaunchAgents/com.git-notes-sync.plist`），支持定时模式（StartInterval + `gns sync-all`，默认 600s）与常驻模式（`--daemon` + KeepAlive）；自动注入 PATH/HOME 环境变量与日志路径；`uninstall` bootout + 删 plist；真机验证通过（含 launchd 环境下真实仓库 fetch/push）。✅ **Linux 已落地（v0.1.4）**：systemd user units（`~/.config/systemd/user/<label>.{service,timer}`，timer 默认 600s / daemon 模式 Restart=always）与 crontab 后端（`--cron`，托管标记块 + @reboot）；unit/cron 生成与 crontab 合并/剥离纯函数有单测，Linux 真机验证通过（Debian 12 WSL2）。✅ **Windows 已落地（v0.1.4）**：任务计划程序（schtasks，MINUTE 间隔 / ONLOGON 常驻，无需管理员）。**黑窗规避（重要）**：任务不能直接注册 `gns.exe`（每次触发会弹黑色控制台窗口）——必须经 `wscript.exe` 运行 `.vbe` 包装脚本隐藏窗口再拉 gns（WshShell.Run(..., 0)），`gns install` 自动生成 `<label>.vbe` 并注册；日志统一 `--log`（`%LOCALAPPDATA%\git-notes-sync\<label>.log`）三平台一致，`gns logs`（tail/跟随/`--path`）可查。日志轮转：`--log` 模式超 `[log] max_size_kb`（默认 500KB）自动切 `.1`（保留 `max_backups` 份）；systemd 由 journald 管理。待办：Linux/Windows autocrlf 等边界实测 |
| P2 | 非 git 目录纳入统一 git 仓库管理 | 可配置多个非 git 目录（桌面/下载/配置目录等），定时增量复制到集中 git 仓库，由该仓库承担版本管理与远端同步。设计要点：配置 `[[sync]]` 映射（源目录 → 集中仓库内子路径）；复用 daemon timer 触发；增量检测（mtime+size 或内容 hash）避免全量拷贝；复制前先 pull 合并远端，复制后 commit + push（走现有同步链路）；删除策略默认不传播（源删除仅记录，防误删），可选 `delete = true`；集中仓库被多端修改时按现有冲突模型处理 |
| P3 | 一键安装脚本 `install.sh`/`install.ps1` | 封装冗长命令（`--install-links=true --foreground-scripts --allow-scripts=git-notes-sync` 参数过多），用户只跑一条命令：检测平台 → 下载对应 tgz/二进制 + 校验 checksum → 安装/提示；也可考虑 curl 直装（不依赖 npm）。注：`gns install`（v0.1.4）已覆盖"注册定时任务"侧的一键化 |
| P3 | 可选：GoReleaser 替代手写 Actions | 多平台发布更成熟（changelog/Homebrew 等）；checksums 已实现（CI 生成 `checksums.txt`），当前 6 平台手写够用 |
| P3 | 可选：shell 补全 | `gns completion bash\|zsh\|fish`，npm 生态用户偏好 |

## 四、npm 分发方案踩坑记录（2026-08 讨论定稿）

Go 二进制 × npm 分发的 5 种方案对比（按尝试顺序）：

| # | 方案 | 机制 | allow-scripts | 优点 | 缺点/坑 | 结论 |
|---|------|------|:---:|------|---------|------|
| ① | **shim + postinstall 下载**（最早版：`bin → notes.js` shim，install.js 下载） | shim spawn 下载的二进制 | ⚠️ **拦**（npm 11 默认拦截一切 install 脚本，需 `--allow-scripts`/`approve-scripts` 放行） | 单包简单；shim 可给友好报错；下载器可运维（镜像/代理/校验） | `npm install github:...` 要求 package.json 在仓库根（否则 ENOENT）；GitHub Releases 下载 302 重定向需手动跟随 | 架构正确，被 allow-scripts 卡住 |
| ② | **无 shim 直接链接**（bin → 固定名 `bin/gns.exe`） | postinstall 下载到固定路径，npm 直接链接原生二进制 | ⚠️ **拦**（同 ①） | 无 JS 中间层；Windows 上 .exe 天然适配 | 二进制缺失（allow-scripts 拦截）时报莫名其妙的系统错误，无友好提示；Linux/mac 上固定名带 `.exe` 别扭 | 不推荐 |
| ③ | **平台分包**（reasonix/esbuild 模式：optionalDependencies 子包 `@aweyonhub/git-notes-sync-<platform>`） | npm 按 os/cpu 自动装对应子包，二进制打进子包发布到 registry | ✅ **不拦**（无 install 脚本） | npm 原生机制；安装即用 | **本质仍是按平台下载二进制**（从 registry 拉子包）；**需发布到 npm registry——本项目只想依赖 GitHub 分发**（6 个子包 + npm token + 版本同步）；files 白名单含不存在的 bin 目录会 TAR_ENTRY_ERROR | 唯一能绕开 allow-scripts 的"下载"方案，但违背"仅 GitHub 分发"的约束 |
| ④ | **懒下载 shim**（候选：install.js 合并进 gns.js，首次运行时下载） | 无 postinstall，shim 检测二进制 → 缺失则下载 → 执行 | ✅ **不拦**（无 install 脚本） | 重装/升级自动重下；单文件入口 | 首次运行需联网（体验比安装期差）；**不能下载到包目录**（sudo 全局安装时普通用户无写权限），须用用户缓存 `~/.cache/git-notes-sync/<version>/`；并发首跑会重复下载；postinstall 里跑 `gns --version` 验证会重新引入 allow-scripts（有脚本即拦），且 bin 链接在 lifecycle 之后创建、`gns` 不在 PATH | 待实现（2026-08 讨论中） |
| ⑤ | **tgz 上传 Release + URL 安装**（候选，2026-08-14） | `npm pack` 生成 `git-notes-sync-<version>.tgz` 上传 GitHub Release，用户 `npm install -g --allow-scripts=git-notes-sync https://github.com/aweyonhub/git-notes-sync/releases/download/v<version>/git-notes-sync-<version>.tgz`；tarball 走标准解包（复制到 node_modules，非 git 依赖链接） | ⚠️ **拦**（postinstall 仍需放行） | **无 git 依赖符号链接问题**（链接到 cacache 临时目录的根源）；无 reify 竞态（postinstall 在真实包目录跑）；与现有下载器/checksum 链路完全复用 | Release 多一个资产；postinstall 仍需 allow-scripts；URL 安装不如 `github:` 直观 | 备选：现状 `--install-links=true` 已解决链接问题；npm 12 若对 git 依赖行为再变时启用 |

**结论**：allow-scripts 是 npm 11 对所有带 install 脚本包的统一策略（esbuild/prisma 同款），与 shim 无关；①②④ 都有 postinstall 即被拦，③ 无脚本不拦但发布复杂。当前代码已切到 **③平台分包**（meta 包无脚本 + 6 个 os/cpu 子包，v0.1.2 分支落地）；①的加固版下载器保留在 `npm/scripts/install.js`（curl 直装 fallback，不再参与 npm 安装）；④ 仍是候选（若想彻底去掉 registry 依赖时启用）。

**方案③发布权限与流程补充（2026-08-14 实测）**：无需任何申请/审核，npmjs.com 免费注册即可发布——public 包无限量免费（private 才付费：Pro/Org）；npm 强制 publish 需 2FA 或带 bypass 的 granular token；scope **无预注册机制**（先发布 `@xxx/yyy` 的账号即获得该 scope，与 GitHub org 无关）；本项目的 scope 直接**注册 npm org `aweyonhub`**（官方保留、无抢注风险，2026-08-18 确定），包名 `@aweyonhub/git-notes-sync` 与 6 个平台子包名均实测可用。CI 自动发布已配置（tag 触发：assemble + 6 子包 + packages/meta 主包，`NPM_TOKEN` secret 认证，bypass-2FA granular token）；**注意**：bypass-2FA token 不能 unpublish（npm 新规则），删除版本需网页操作或 2FA。发布流程：先 6 子包再主包，`--access public`，版本全部对齐（assemble 单点输入）；子包二进制由 CI 交叉编译注入（Linux 打包保留 755 执行位，Windows 手动打包会丢执行位——assemble 已加 chmod +x 防御）。

**npm registry CI 发布配置（3 步走通）**：

1. **注册 npm 账号与 org**：https://www.npmjs.com/signup 注册账号（免费）；https://www.npmjs.com/org/new 创建 org（如 `aweyonhub`），org 名即 scope 前缀（`@aweyonhub/xxx`）。
2. **生成 Granular Access Token（不带 2FA）**：https://www.npmjs.com/settings/~/tokens → Generate New Token → **Granular Access Token**；权限选 "Read and write"；scopes 勾选你的 org（如 `aweyonhub`）；**关键**：取消勾选 "Require two-factor authentication"（这样 token 不需要 2FA 就能 publish，适合 CI 无人值守场景）。生成后复制 token（形如 `npm_xxxxxxxxxxxxxxxxxxxx`）。
3. **配置 GitHub Actions Secret**：进入仓库 https://github.com/<owner>/<repo>/settings/secrets/actions → 选择 **Repository secrets**（不是 Environment secrets）→ New repository secret；Name 填 `NPM_TOKEN`，Value 填上一步复制的 token；保存。CI workflow 中 `setup-node` 会读取这个 secret 自动登录 npm，`npm publish` 即可免交互发布。

配置完成后，打 `v*` tag 推送到 main，GitHub Actions 会自动：交叉编译 6 平台二进制 → 上传 GitHub Release → assemble 子包 → `npm publish` 7 个包（6 子包 + 1 主包）。全程无需人工介入。

**npm 脚本拦截机制演进（重要，2026-08 实测确认）**：

| 版本 | 机制 | 放行方式 |
|------|------|---------|
| npm 11 | `allow-scripts` 配置（连字符，全局 config） | `--allow-scripts=<pkg>` 或 `npm config set allow-scripts=<pkg> --location=user` 或 `npm approve-scripts` |
| npm 12（2026-07-08 发布） | **`allowScripts` policy（驼峰，root 包策略）**——`allow-scripts` config 已失效 | 先 `npm install-scripts approve` 记录批准，再 **`npm rebuild`** 才真正执行脚本 |

- **`npm rebuild` 语义**：默认 rebuild **当前目录项目的所有依赖**；全局包必须 `npm rebuild -g <pkg>`（只 rebuild 指定包，即重跑其 install 脚本）
- **兜底**：任何版本都可用 `node <安装路径>/npm/scripts/install.js` 手动执行下载器，绕开全部拦截机制
- 结论：npm 对 install 脚本的管控持续收紧，**无脚本方案（③④）的免疫价值在上升**

**其他踩坑**（已修复）：
- `npm install github:...` 打包：package.json 必须在仓库根（`npm/` 子目录 → ENOENT）
- GitHub Releases 下载 302 → CDN，Node http 默认不跟随（需手动 redirect）
- `files` 白名单含打包时不存在的目录 → TAR_ENTRY_ERROR
- npm 对 git 依赖的 reify 竞态：跑 postinstall 时目录被 move → `spawn sh ENOENT`（npm bug，Linux/Windows 都会撞，概率性；CI 用 pack + tgz 绕过验证）。**规避路径**：让 postinstall 被 allow-scripts 拦（不跑即不撞），再 `npm approve-scripts <pkg>` + `npm rebuild -g <pkg>` 在稳定目录执行
- npm 缓存/残留导致包内容混合：多次安装不同结构版本后，可能出现 package.json 新、文件旧（MODULE_NOT_FOUND）。处理：`npm cache clean --force` + 删除 `node_modules/git-notes-sync` 后重装
- **git 依赖符号链接（根源级发现，2026-08-14）**：npm 对 `github:` 语法默认符号链接到 `cacache/tmp/git-cloneXXX`（`npm list -g` 显示 `-> ...\git-cloneXXX`），临时目录被清后包失效——这才是 MODULE_NOT_FOUND/竞态的根源。**修复：`--install-links=true --foreground-scripts`**（强制复制解包 + 前台跑脚本）。实测：`--install-links=false` 安装失败或留下失效链接；`true` 装成普通目录一切正常。完整命令：`npm install -g --install-links=true --foreground-scripts --allow-scripts=git-notes-sync github:aweyonhub/git-notes-sync`
- shim 必须有 shebang（`#!/usr/bin/env node`），否则 bash 当 shell 解析
- .gitignore 通配符误伤源文件（`gns*` 曾忽略 `gns.js`）
- **macOS 覆盖 npm 包内二进制 → 执行被 SIGKILL（2026-08-19 实测）**：本地升级 npm 全局包的手动替换（如 `cp ./gns` 覆盖 `~/.local/lib/node_modules/@aweyonhub/git-notes-sync/node_modules/<platformpkg>/bin/gns`）会导致 `gns` 命令无输出 exit 1、直接跑二进制 `Killed: 9`（137）。根因：覆盖保留了原 inode 上的 macOS provenance 元数据（npm 下载来源标记），Sequoia Gatekeeper 对"来源标记 + 内容被替换"的可执行直接 SIGKILL（`xattr -d com.apple.provenance` 无效，临时目录全新 cp 的文件就不受影响）。**修复/规避：先 `rm` 再 `cp`（新 inode）**；发行版升级请走 npm（`npm install @aweyonhub/git-notes-sync@<ver> -g`），本机二进制覆盖仅用于未发版的本地快照

**CI npm publish 踩坑**（v0.1.3，2026-08-18 首次自动发布，4 轮失败后修复，详见 [ci.yml](https://github.com/aweyonhub/git-notes-sync/blob/main/.github/workflows/ci.yml)）：

| # | 现象 | 根因 | 修复 |
|---|------|------|------|
| 1 | assemble 脚本 Permission denied（exit 126） | `.sh` 文件 git 权限位是 `100644`（无 +x），Linux runner checkout 后不可执行 | `git update-index --chmod=+x` 设置 `100755` |
| 2 | npm publish 报 ENEEDAUTH | `setup-node` 没配 `registry-url`，`NODE_AUTH_TOKEN` 写了但 npm 不知道往哪个 registry 认证 | `setup-node` 加 `registry-url: 'https://registry.npmjs.org'` |
| 3 | 部分子包报 E409 Conflict（"Failed to save packument"） | 连续发 7 个包太快，npm 内部状态没同步完就撞上竞态 | 每个包发布后 sleep 5s，加 3 次重试 + 10 秒间隔 |
| 4 | 重试时已发布的包报 403（"cannot publish over"） | 前几次 CI 失败但部分包已发成功，全量重发撞版本冲突 | 发布前 `npm view` 检查版本是否已存在，已存在则 skip |

---

## 五、风险与已知边界

- **二进制冲突"保留本地副本"** 意味着远端版本被覆盖——属规格内决策（`binary_strategy=ours`），重要二进制建议改 `abort`
- **max_wait 强制提交** 可能在文件写入中途截断内容（规格明确接受的兜底行为）
- **merge 阻塞场景**（auto_commit=off + 工作区脏 + 远端更新同文件）：跳过该仓库并提示，需人工 `gns commit` 后重试
- **AI 语义合并质量**不可控，`resolve --ai` 输出建议人工复核后提交（当前即如此：写盘→可 git diff 检查→提交）
