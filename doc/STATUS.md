# git-notes-sync 开发状态

> 更新：2026-08-13 · 基于 [git-notes-sync.md](../git-notes-sync.md) 规格开发（规格第七节已固化实现决策）

---

## 一、确认内容

### 1.1 文档开放问题（§六）→ 已给出方案

| # | 开放问题 | 采用方案 | 状态 |
|---|---------|---------|------|
| 1 | 是否内置纯 Go Git 实现 | 不做，调用系统 Git（规格 §1.3 非目标），`internal/git` 留替换扩展点 | ✅ 已落地 |
| 2 | 冲突批量语义解决的调度方式 | `gns resolve --ours/--theirs/--ai` 手动触发；daemon 不做自动语义解决；AI 失败保留 markers 不丢数据 | ✅ 已落地 |
| 3 | 定时任务间隔推荐值 | daemon `sync_interval=60s`（最小 5s）；cron 建议 `*/5 * * * *` | ✅ 已落地 |

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

- [ ] **GitHub 仓库地址**：npm 壳与 Actions 中 `git-notes-sync/git-notes-sync` 为占位符
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
| daemon | 轻量 timer（默认 60s）；配置 mtime 变更热重载；多仓库 `repos` 遍历；`--once` 单次 | ✅ 冒烟 |
| CLI | sync / commit / commit-ai / status / resolve / daemon / version / help | ✅ 冒烟 |

### 2.2 测试

- **9 个集成测试**（真实 git 双仓库场景）：快进合并+push、文本冲突保留+resolve 回写远端、二进制冲突、debounce 三态、AI fallback、resolve AI 未配置、无 upstream、非仓库
- **5 个单测**：marker 解析（ours/theirs/多块/未闭合/CRLF）
- 全部通过；`go vet`、`gofmt` 干净

### 2.3 分发与工程化

- npm 壳：`npm/`（postinstall 按平台下载 Releases 二进制，bin 注册 notes/notes-sync）
- GitHub Actions：tag 触发交叉编译 5 平台 + 发布 Release
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
| P0 | 创建 GitHub 仓库并替换占位符 | `npm/scripts/install.js`、`npm/package.json`、workflow 中的 `git-notes-sync/git-notes-sync` |
| P0 | 打 tag 发布 v0.1.0 | 触发 Actions 交叉编译 → 生成 5 平台二进制 Release |
| P0 | 验证 `npm install -g github:...` 全链路 | 在干净环境（无 Go）实测 postinstall 下载 + bin 可用 |
| P1 | 真实 AI endpoint 冒烟 | 本地 Ollama 或任意 OpenAI-compatible 服务验证 `commit_message="ai"` 与 `resolve --ai` |
| P1 | Windows 实机验证 | daemon 行为、credential helper / SSH 环境继承、autocrlf 场景 |
| P2 | `gns resolve` 交互式模式 | 逐个文件选择 ours/theirs/AI（当前为全局 flag 一次性处理） |
| P2 | cron 示例文档完善 | README 已有 `*/5 * * * *` 示例，可补充 launchd plist 模板 |
| P3 | 可选增强 | `gns init` 生成示例配置；`--repo` 支持多仓库批量 sync |
| P3 | 可选：GoReleaser 替代手写 Actions | 多平台发布更成熟（checksums/changelog/Homebrew 等）；当前 5 平台手写够用，仅建议补充 checksums 生成 |
| P3 | 可选：shell 补全 | `gns completion bash\|zsh\|fish`，npm 生态用户偏好 |

## 四、风险与已知边界

- **二进制冲突"保留本地副本"** 意味着远端版本被覆盖——属规格内决策（`binary_strategy=ours`），重要二进制建议改 `abort`
- **max_wait 强制提交** 可能在文件写入中途截断内容（规格明确接受的兜底行为）
- **merge 阻塞场景**（auto_commit=off + 工作区脏 + 远端更新同文件）：跳过该仓库并提示，需人工 `gns commit` 后重试
- **AI 语义合并质量**不可控，`resolve --ai` 输出建议人工复核后提交（当前即如此：写盘→可 git diff 检查→提交）
