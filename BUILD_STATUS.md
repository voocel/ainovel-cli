# 构建与验证状态

更新时间：2026-08-01

## 当前能力

- CLI、TUI 与 Wails desktop 共用同一套 Host、Store 和模型路由。
- desktop 已支持书库、长篇/短篇创作、阅读、导入、拆文、扫榜、仿写画像、章节验收、去 AI 味检查、封面生成与导出。
- 图片生成的 Base URL、API Key、模型和尺寸统一由“设置 > 图片生成”管理。
- 短篇模式会在启动裁定、Store 和路由层禁用长篇分层结构。
- 拆文与扫榜使用可恢复的工件管线，并通过输入摘要避免错误复用旧产物。

## 最近验证

以下检查已通过：

```powershell
go test ./...
go vet ./...
go test -race ./internal/host/scan ./internal/host/rip ./cmd/ainovel-desktop
cd cmd/ainovel-desktop/frontend
npm run build
```

Windows desktop 已通过 Wails v2 生产构建。标准构建命令为：

```bash
bash scripts/build-desktop.sh
```

默认产物为 `cmd/ainovel-desktop/build/bin/ainovel-desktop.exe`。如果已有运行实例锁定该文件，按 `AGENTS.md` 使用新的时间戳文件名构建，不终止用户进程。

## 不入库内容

本地配置、API Key、小说运行数据、日志、前端依赖目录和 desktop 二进制产物均由 `.gitignore` 排除。
