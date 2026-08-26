# AGENTS.md

> 本文件为 AI 工具统一入口。完整项目规范与约束请务必阅读 [README.md](README.md)。

## Agent准则

- 未经用户同意或明确指定，禁止自行 commit / push，修改只保留在工作区

## 版本修改

- 版本号**只改根 `package.json` 一处**；`make version` 同步 version.go；子包/meta 打 tag 时由 CI `assemble-platform-packages.sh <tag>` 自动同步——勿手动改