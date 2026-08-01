# 短篇小说实现说明

## 用户入口

desktop 新建创作时可选择长篇小说或短篇小说。配置文件也支持：

```jsonc
{
  "genre": "short_story"
}
```

未设置 `genre` 时保持原有长篇行为。

## 运行链路

1. `domain.Genre` 定义 `novel` 与 `short_story`，并持久化到 `Progress.Genre`。
2. Host 启动时把界面选择或配置值传入启动裁定器。
3. `genre=short_story` 强制使用 `architect_short`，不再依赖需求原文是否写出“短篇”。
4. Store 拒绝为短篇保存分层大纲，并持久化修复旧项目中错误的 `Layered`、`CurrentVolume` 和 `CurrentArc`。
5. Flow 路由把短篇固定到短篇规划层级，不执行卷弧扩展。
6. Writer、Editor 和上下文构建器接收 genre，按短篇的单一冲突、紧凑场景和自然收束策略工作。

## 数据兼容

- 旧项目缺少 `genre` 时按 `novel` 处理。
- 已被错误标记为分层的短篇项目，在加载路由状态时会持久化归一化。
- 短篇写入 `layered_outline`、`expand_arc` 或 `append_volume` 会被 Store/工具层拒绝。

## 自动化覆盖

测试覆盖 genre 解析、短篇规划师裁定、Progress 初始化与归一化、分层大纲拒绝、Flow 路由、上下文注入和 desktop 创建入口。全量验证命令见 `BUILD_STATUS.md`。
