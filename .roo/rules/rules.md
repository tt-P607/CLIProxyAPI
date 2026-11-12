# CLI Proxy API 项目开发规范 (Development Rules)

本文档记录了 CLI Proxy API 项目的核心架构、开发规范及构建指南，旨在帮助 AI 助手快速进入开发状态，减少环境调查导致的 Token 浪费。

## 1. 项目概览 (Project Overview)

- **定位**: 基于 Go 1.26+ 的多协议代理服务器，提供 OpenAI/Gemini/Claude/Codex 兼容接口。
- **项目状态**: 当前为 **客制化 Fork 版本**，包含针对特定部署环境的定制修改。
- **核心功能**: OAuth 认证集成、轮询负载均衡、请求签名/缓存、用法统计及插件系统。
- **仓库地址 (Fork)**: [https://github.com/tt-P607/CLIProxyAPI](https://github.com/tt-P607/CLIProxyAPI)
- **上游仓库**: [https://github.com/router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)

## 2. 核心架构 (Architecture)

- `internal/thinking/`: 推理管道核心。遵循"规范化表示 (`ThinkingConfig`) → 供应商特定翻译 (`ProviderApplier`)"的设计模式。
- `internal/runtime/executor/`: 各供应商的运行时执行器。辅助文件需放在子目录 `helps/` 下。
- `internal/translator/`: 供应商协议转换器。**注意**: 除非是更广泛更改的一部分，否则不要单独修改此目录；且需确认具备 `WRITE` 权限。
- `internal/registry/`: 模型注册表与远程更新逻辑。
- `internal/api/`: 基于 Gin 的 HTTP 路由与中间件。
- `static-frontend/`: 基于 React & TypeScript 的管理后台前端。

## 3. 开发约束 (Coding Conventions)

- **简洁原则**: 保持代码简单 (KISS)，避免过度工程。
- **语言规范**:
  - 仅使用 **英文注释**。若修改已有中文注释，请将其翻译为英文。
  - 用户可见字符串保留原文件使用的语言（如 `i18n` 文件）。
- **错误处理**:
  - **严禁使用 `log.Fatal` / `log.Fatalf`**（会导致容器退出），应返回 `error` 并使用 `logrus` 记录。
  - 推荐使用 `fmt.Errorf("context: %w", err)` 包装错误。
- **变量命名**: 避免变量影子化，冲突时增加后缀（如 `errStart := ...`）。
- **资源清理**: 包装 `defer` 中的错误处理，确保文件/连接正确关闭。

## 4. 构建与环境 (Build & Environment)

- **环境**: Windows 10 (宿主), Docker (运行环境), PowerShell (默认 Shell)。
- **后端构建**: 必须通过 Docker Compose 执行，以确保 CGO 等 Linux 构建环境一致性。
  ```powershell
  docker compose up --build -d
  ```
- **前端构建**:
  1. 进入 `static-frontend` 执行 `npm run build`。
  2. 将 `dist/index.html` 复制并重命名为顶层 `static/management.html`：
     ```powershell
     copy /Y dist\index.html ..\static\management.html
     ```

## 5. 提示词建议 (System Prompt Integration)

在处理任务前，请优先阅读 `AGENTS.md` 和本文档。对于涉及 `internal/translator/` 的修改，务必先检查 GitHub 权限。
