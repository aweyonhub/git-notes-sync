# 可复用资产清单（REUSABLE）

> 本文件沉淀 git-notes-sync 项目中可整体搬运到新项目（尤其 Go CLI / 常驻工具）的四类资产：
> **文档组织、npm 发包、跨平台 service 注册、配置/调度骨架**。
> 新项目照此清单复制结构，替换业务即可起步。

---

## 1. 文档组织（五件套）

多轮开发验证的最佳分工：约束 → 实现 → 状态 → 使用 → AI 协作，各自职责单一、互相引用。

| 文档 | 职责 | gns 中的位置 | 骨架内容 |
|------|------|-------------|---------|
| **AGENTS.md** | AI 协作约束（提交纪律、版本单点、命名规范） | 仓库根 | "禁止自行 commit/push"+"版本只改根 package.json 一处" |
| **spec（规格）** | 设计决策、实现约束（先定调再动手） | `doc/git-notes-sync.md`（§七 实现决策表） | 决策表：每行经典"决策点 + 结论 + 理由" |
| **STATUS** | 已完成/待办/踩坑记录（可信历史） | `doc/STATUS.md` | 确认内容、已完成、待办、**踩坑记录表**（现象/根因/修复） |
| **USAGE** | 使用文档（命令/配置/FAQ，只讲用法不写实现） | `doc/USAGE.md` | 安装、命令详解、配置表、定时调度、FAQ、**卸载章节** |
| **SKILL.md** | AI 读的"项目使用手册"（代替每次重读文档） | `skills/git-notes-sync/SKILL.md` | frontmatter(name/description) + 功能/安装/命令/配置/FAQ |

**复用步骤**：新项目先建 AGENTS.md（2~3 条硬约束）+ spec 决策表 + STATUS 空模板 + USAGE 骨架；功能完成一节填一节，踩坑同步进 STATUS。SKILL 在功能稳定后从 USAGE 提炼。

---

## 2. npm 发包处理（CLI 分发标准件）

"meta 包 + 平台分包 + 单点版本 + CI 自动发布 + 双源兜底"的完整流水线。

### 结构

```
packages/
  meta/                    # 主包：无二进制，bin=gns.js shim，optionalDependencies 锁 6 平台子包
  cli-<os>-<arch>/         # 平台子包：os/cpu 字段 + 原生二进制（由 CI 交叉编译注入）
scripts/
  assemble-platform-packages.sh <version>   # 单点版本：用 tag 版本重写 meta+deps+全部子包
scripts/genversion/main.go                  # go generate：从根 package.json 生成 version.go
.github/workflows/ci.yml                    # tag 触发：build(6平台) → publish(幂等) → Release
```

### 关键机制（照抄点）

- **单点版本**：根 `package.json` 是唯一手动改点；打 tag 时 CI 用 `assemble-platform-packages.sh <tag版本>` 统一重写子包/meta/deps（子包版本书面值只是"占位"）
- **meta shim**：`bin/gns.js`（node）通过 `require.resolve('@scope/pkg-<platform>-<arch>/bin/xxx')` 定位平台二进制再 spawn；npm 用 optionalDependencies + os/cpu 过滤自动选平台
- **发布顺序**：先 6 子包再 meta；幂等（`npm view ... version` 存在则 skip）；3 次重试
- **CI 发布**：`secrets.NPM_TOKEN`（bypass-2FA granular token——**注意该 token 不能 unpublish**）；`--access public`
- **双源兜底**：GitHub 直装源（仓库根 package.json postinstall 下载器）保留，npm registry 挂掉/审查时仍可 `npm install github:...#tag` 安装
- **Release 侧**：`softprops/action-gh-release` + `generate_release_notes: true`（自动 changelog + 6 二进制 + checksums.txt，与 npm 发布互补）

### 已知坑（从 STATUS 抄）

- npm 对 git 依赖默认符号链接 cacache 临时目录 → `--install-links=true --foreground-scripts --allow-scripts=<pkg>`
- macOS 覆盖 npm 包内二进制（cp 覆盖保留 provenance 元数据）→ 执行 SIGKILL；须 `rm` 后新文件再 `cp`
- unpublish 限制：发布超 72h 不能撤同版本；镜像站（npmmirror）同步有分钟级延迟

---

## 3. 跨平台 service 注册（开机自启标准件）

`internal/service` 整包可移植：统一 Install/Uninstall/Loaded 接口 + 四平台后端 + 日志轮转 + 统一查看。

### 接口抽象

```go
type LaunchOptions struct { Label, Exe, Mode(interval|daemon), Backend, Interval, Config, Home, LogDir, Force }
Install(o) / Uninstall(o) / Loaded(o) / DefaultLogDir(home) / Preflight(home, repos)（凭据/空列表/TCC 预检）
```

### 四后端（build tag 分文件）

| 平台 | 文件 | 机制 |
|------|------|------|
| macOS | `launchd.go` (darwin) | launchctl bootstrap/bootout；plist `StartInterval`（定时）/ `KeepAlive`（daemon）+ PATH/HOME 注入 |
| Linux | `linux.go` | systemd user units（timer/service Restart=always）/ `--cron` crontab（托管标记块 + @reboot） |
| Windows | `windows.go` | schtasks MINUTE / ONLOGON + **wscript/.vbe 隐藏窗口**（直接注册 exe 会弹黑窗） |
| 其他 | `other.go` | 桩报错 |

### 配套（一起搬）

- **`--log` 统一日志**：main 解析 `--log path`（及 `--log=path`）→ stdout/stderr 重定向 + 按 `[log] max_size_kb/max_backups` 轮转；daemon 每轮循环再轮转（`GNS_LOG_FILE` 环境变量传递）
- **`logs` 命令**：`-n N` / `-f`（默认 20 行窗口再跟随，tail 语义）/ `--path` 打印路径 / `--label` 指定任务；Linux systemd 自动路由 `journalctl`
- **interval 优先级**：`-interval` flag > 全局配置 `sync_interval` > 默认 600s
- **Preflight 预检**：安装后提示 credential.helper 缺失 / repos 为空 / TCC 保护目录（只警告不阻断）
- **卸载顺序纪律**：先 `uninstall` 后卸载 npm 包（否则告警/僵尸任务）——FAQ 与 USAGE §1.1 有完整流程

---

## 4. 配置与调度骨架（常驻工具标配）

- **配置合并层级**：默认值 ← 全局配置 ← `-c` 显式 ← 仓库级覆盖（gns = `.notes-sync.toml`）；环境变量 `GNS_CONFIG` 覆盖全局路径
- **daemon 模式**：timer 循环 + 配置 mtime 热重载（改配置免重启）+ 每轮同步多仓库
- **跨进程锁**：`internal/lock`（带过期；防并发同步）
- **重试**：`internal/retry`（指数退避 + 按 error 分类判断是否可重试）
- **git 封装**：`internal/git`（Runner + 单命令超时 + 常用原语）

---

## 复用 checklist（新项目起步）

- [ ] 复制五件套文档骨架，AGENTS.md 写 2~3 条硬约束
- [ ] 复制 `packages/` + assemble/genversion 脚本 + CI publish job（改包名/scope）
- [ ] 复制 `internal/service` 整包（日志/注册/查看三件套随包走）
- [ ] 复制 lock/retry/git 封装（如业务涉及 git/网络/并发）
- [ ] 跑通本地：`make cross` + assemble + 单机 install/uninstall 三平台冒烟