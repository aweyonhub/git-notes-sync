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
| 配置文件位置 | 仓库 `.notes-sync.toml` + 全局 `~/.config/git-notes-sync/config.toml`，仓库覆盖全局 |
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
| AI 集成 | OpenAI-compatible API + 任意 CLI 双后端；stdin=diff / stdout=message；diff 截断 50KB；任何故障 fallback（`ai_fallback`） | ✅ fallback 测试 |
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

- npm 分发（当前，方案③平台分包）：meta 包（仓库根 package.json，无 install 脚本，bin=bin/gns.js shim，optionalDependencies 锁 6 个平台子包）+ `packages/cli-<os>-<arch>`（os/cpu 字段 + 原生二进制）；发布流程 `make cross && scripts/assemble-platform-packages.sh <ver>`（单点版本同步）→ 先 6 子包后主包 `npm publish --access public`；`github:` 直装仍可用（`--install-links=true`，子包从 registry 解析）
- npm 壳（历史，方案①）：仓库根即 npm 包（postinstall 按平台下载 Releases 二进制）；下载器保留在 `npm/scripts/install.js`（curl 直装 fallback）
- GitHub Actions：tag 触发交叉编译 6 平台 + 发布 Release
- Makefile：build / test / vet / cross / clean
- README.md + example.config.toml（全量配置注释）
- 规格更新：git-notes-sync.md 追加 §七 实现决策（19 项）

### 2.4 开发环境

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
| P1 | Windows 实机验证 | daemon 行为、credential helper / SSH 环境继承、autocrlf 场景 |
| P2 | `gns resolve` 交互式模式 | 逐个文件选择 ours/theirs/AI（当前为全局 flag 一次性处理） |
| P2 | cron 示例文档完善 | USAGE §5 已有 cron/launchd/Windows 三种示例，可补充 launchd plist 完整模板 |
| P2 | `gns install` / `gns uninstall` 注册/管理系统服务 | 一键注册 daemon 开机自启并反注册：Linux systemd user unit（`~/.config/systemd/user/gns.service`）；macOS launchd LaunchAgent（`~/Library/LaunchAgents/com.git-notes-sync.plist`）；Windows 任务计划程序（`schtasks /Create`，可选真服务需 nssm / x/sys/windows/svc）；`uninstall` 删除对应注册并停止 |
| P2 | 非 git 目录纳入统一 git 仓库管理 | 可配置多个非 git 目录（桌面/下载/配置目录等），定时增量复制到集中 git 仓库，由该仓库承担版本管理与远端同步。设计要点：配置 `[[sync]]` 映射（源目录 → 集中仓库内子路径）；复用 daemon timer 触发；增量检测（mtime+size 或内容 hash）避免全量拷贝；复制前先 pull 合并远端，复制后 commit + push（走现有同步链路）；删除策略默认不传播（源删除仅记录，防误删），可选 `delete = true`；集中仓库被多端修改时按现有冲突模型处理 |
| P3 | 一键安装脚本 `install.sh`/`install.ps1` | 封装冗长命令（`--install-links=true --foreground-scripts --allow-scripts=git-notes-sync` 参数过多），用户只跑一条命令：检测平台 → 下载对应 tgz/二进制 + 校验 checksum → 安装/提示；也可考虑 curl 直装（不依赖 npm） |
| P3 | 可选：GoReleaser 替代手写 Actions | 多平台发布更成熟（changelog/Homebrew 等）；checksums 已实现（CI 生成 `checksums.txt`），当前 6 平台手写够用 |
| P3 | 可选：shell 补全 | `gns completion bash\|zsh\|fish`，npm 生态用户偏好 |

## 四、npm 分发方案踩坑记录（2026-08 讨论定稿）

Go 二进制 × npm 分发的 5 种方案对比（按尝试顺序）：

| # | 方案 | 机制 | allow-scripts | 优点 | 缺点/坑 | 结论 |
|---|------|------|:---:|------|---------|------|
| ① | **shim + postinstall 下载**（最早版：`bin → notes.js` shim，install.js 下载） | shim spawn 下载的二进制 | ⚠️ **拦**（npm 11 默认拦截一切 install 脚本，需 `--allow-scripts`/`approve-scripts` 放行） | 单包简单；shim 可给友好报错；下载器可运维（镜像/代理/校验） | `npm install github:...` 要求 package.json 在仓库根（否则 ENOENT）；GitHub Releases 下载 302 重定向需手动跟随 | 架构正确，被 allow-scripts 卡住 |
| ② | **无 shim 直接链接**（bin → 固定名 `bin/gns.exe`） | postinstall 下载到固定路径，npm 直接链接原生二进制 | ⚠️ **拦**（同 ①） | 无 JS 中间层；Windows 上 .exe 天然适配 | 二进制缺失（allow-scripts 拦截）时报莫名其妙的系统错误，无友好提示；Linux/mac 上固定名带 `.exe` 别扭 | 不推荐 |
| ③ | **平台分包**（reasonix/esbuild 模式：optionalDependencies 子包 `@git-notes-sync/cli-<platform>`） | npm 按 os/cpu 自动装对应子包，二进制打进子包发布到 registry | ✅ **不拦**（无 install 脚本） | npm 原生机制；安装即用 | **本质仍是按平台下载二进制**（从 registry 拉子包）；**需发布到 npm registry——本项目只想依赖 GitHub 分发**（6 个子包 + npm token + 版本同步）；files 白名单含不存在的 bin 目录会 TAR_ENTRY_ERROR | 唯一能绕开 allow-scripts 的"下载"方案，但违背"仅 GitHub 分发"的约束 |
| ④ | **懒下载 shim**（候选：install.js 合并进 gns.js，首次运行时下载） | 无 postinstall，shim 检测二进制 → 缺失则下载 → 执行 | ✅ **不拦**（无 install 脚本） | 重装/升级自动重下；单文件入口 | 首次运行需联网（体验比安装期差）；**不能下载到包目录**（sudo 全局安装时普通用户无写权限），须用用户缓存 `~/.cache/git-notes-sync/<version>/`；并发首跑会重复下载；postinstall 里跑 `gns --version` 验证会重新引入 allow-scripts（有脚本即拦），且 bin 链接在 lifecycle 之后创建、`gns` 不在 PATH | 待实现（2026-08 讨论中） |
| ⑤ | **tgz 上传 Release + URL 安装**（候选，2026-08-14） | `npm pack` 生成 `git-notes-sync-<version>.tgz` 上传 GitHub Release，用户 `npm install -g --allow-scripts=git-notes-sync https://github.com/aweyonhub/git-notes-sync/releases/download/v<version>/git-notes-sync-<version>.tgz`；tarball 走标准解包（复制到 node_modules，非 git 依赖链接） | ⚠️ **拦**（postinstall 仍需放行） | **无 git 依赖符号链接问题**（链接到 cacache 临时目录的根源）；无 reify 竞态（postinstall 在真实包目录跑）；与现有下载器/checksum 链路完全复用 | Release 多一个资产；postinstall 仍需 allow-scripts；URL 安装不如 `github:` 直观 | 备选：现状 `--install-links=true` 已解决链接问题；npm 12 若对 git 依赖行为再变时启用 |

**结论**：allow-scripts 是 npm 11 对所有带 install 脚本包的统一策略（esbuild/prisma 同款），与 shim 无关；①②④ 都有 postinstall 即被拦，③ 无脚本不拦但发布复杂。当前代码已切到 **③平台分包**（meta 包无脚本 + 6 个 os/cpu 子包，v0.1.2 分支落地）；①的加固版下载器保留在 `npm/scripts/install.js`（curl 直装 fallback，不再参与 npm 安装）；④ 仍是候选（若想彻底去掉 registry 依赖时启用）。

**方案③发布权限与流程补充（2026-08-14 实测）**：无需任何申请/审核，npmjs.com 免费注册即可发布——public 包无限量免费（private 才付费：Pro/Org）；npm 强制 publish 前开启 2FA（账号安全要求，非门槛）；scope **无预注册机制**（先发布 `@git-notes-sync/xxx` 的账号即获得该 scope，与 GitHub org 无关）；已实测 `git-notes-sync` 与 `@git-notes-sync/cli-linux-x64` 等名字均未被占用。CI 自动发布用 granular access token（automation 类型，npm 网站生成）；**当前为手动发布**（`make cross && scripts/assemble-platform-packages.sh <ver> && npm publish`），账号与 token 就绪后补 publish workflow。发布流程：先 6 子包再主包，`--access public`，版本全部对齐（assemble 单点输入）；子包二进制由 CI 交叉编译注入。

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

---

## 五、风险与已知边界

- **二进制冲突"保留本地副本"** 意味着远端版本被覆盖——属规格内决策（`binary_strategy=ours`），重要二进制建议改 `abort`
- **max_wait 强制提交** 可能在文件写入中途截断内容（规格明确接受的兜底行为）
- **merge 阻塞场景**（auto_commit=off + 工作区脏 + 远端更新同文件）：跳过该仓库并提示，需人工 `gns commit` 后重试
- **AI 语义合并质量**不可控，`resolve --ai` 输出建议人工复核后提交（当前即如此：写盘→可 git diff 检查→提交）
