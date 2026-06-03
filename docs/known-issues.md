# 已知问题（Known Issues）

记录已知的兼容性限制与待办，供后续维护参考。

---

## 1. reasoning（thinking）模型在多轮 tool-calling 下不可用

**状态**：待修（框架层 `voocel/litellm` / `voocel/agentcore`）
**发现于**：2026-06-03，多人格竞稿功能（PR #1）真实 LLM 端到端测试

### 现象

当所配模型路由到 **reasoning（thinking）类模型**（如 DeepSeek-R1 / V3-thinking）时，多轮工具循环报错：

```
HTTP 400 [litellm:openai:validation] bad request:
Error from provider (DeepSeek): The `reasoning_content` in the thinking mode must be passed back to the API.
```

### 根因

DeepSeek 等 reasoning 模型要求：多轮对话中，上一轮 assistant 消息的 `reasoning_content` 字段必须在下一轮请求里**回传**。当前 litellm / agentcore 的多轮工具循环在组装后续请求时**未携带** `reasoning_content`，被 provider 拒绝。

关键区分：

- **单轮调用成功**（如 persona 文风生成）——无需回传历史 reasoning
- **多轮工具循环失败**（architect / coordinator / writer / editor 等长循环）——第二轮起就要回传上一轮 `reasoning_content`

这是**框架层**（litellm/agentcore 的消息组装）的限制，与具体业务功能无关。任何能正常多轮 tool-calling 的非 reasoning 模型都能完整跑通（实测 `gpt-5.4-mini`、以及路由到非 thinking 后端的 `kimi-k2.6` 均正常）。

### 影响

无法使用以 thinking 模式运行的 reasoning 模型作为 coordinator / architect / writer / editor。

### 修复方向

在 agentcore/litellm 组装多轮请求时，把上一轮 assistant 消息的 `reasoning_content` 一并带入下一轮（针对支持该字段的 provider）。需先确认 litellm 的 message 结构是否保留了该字段。

### 临时规避

在 provider / 网关侧把模型路由到**非 thinking 后端**即可正常使用。
