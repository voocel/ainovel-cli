# v1 产品方向：创作资产与个性化

> 状态：Planned Product Direction
>
> 约束：遵循 [`../v1-architecture-plan.md`](../v1-architecture-plan.md) 的 Project Overlay、Creator Profile、Novel Pack、Prompt Compiler 与用户确认边界。

## 1. 产品目标

让用户能够用自己的写法持续创作，并把经过确认的风格、规则、示例和题材方法复用到下一本书，而不是每次重新编写长提示词。

## 2. 从 v0 保留什么

保留用户价值：自然语言规则、书级与全局风格、参考文章分析、可切换写作风格，以及修改正文后沉淀个人偏好。

不迁移 v0 依赖目录覆盖顺序和直接替换 Prompt 文件的机制。v1 中可复用资产必须有身份、Revision、来源、作用域、Diff 和明确启用状态。

## 3. 三类产品资产

- Project Overlay：只服务当前作品，表达本书专属写作要求；
- Creator Profile：用户跨书积累的已确认偏好、正例和反例；
- Novel Pack：可以安装、分享和固定版本的题材方法、Rubric、模板与参考资料；官方内置与外部安装使用同一格式和启用规则。

用户看到的是统一的“创作设置与素材库”，但系统仍保留三者不同的生命周期和权威边界。

## 4. 用户旅程

### 为一本书定制

用户用自然语言添加规则，预览它实际进入哪些 Worker、是否与现有规则冲突，然后保存为 Project Overlay 新 Revision。运行中的 Operation 不热替换，用户可以选择让下一任务生效或显式 restart。

### 跨书学习

系统只从用户真实改稿生成候选，展示 before/after 证据与推断规则。用户可以确认、编辑、改变作用域或拒绝；拒绝的候选不再重复打扰。未确认候选绝不进入 Prompt。

### 安装和分享 Pack

用户可以安装本地或 URL Pack，查看来源、digest、Slot、规则、参考资料和 eval，再选择对某本书启用并固定 Revision。安装不等于启用，升级不自动追随 latest。

## 5. 产品能力

- 资产库：查看来源、版本、作用域、启用作品和历史；
- 生效预览：显示最终 Prompt 来源，而不是只显示“已启用”；
- 冲突与 lint：明确哪些规则互相冲突或对当前 Worker 无效；
- Diff 与回滚：资产修改和 Project 引用变化都可检查、可恢复；
- 样本检索：按当前任务选少量高相关正反例，不把整个 Profile 放进稳定前缀；
- Eval：Pack 或 Overlay 变更可用固定样例比较结果，不把单次主观感觉当成升级依据。

## 6. 能力层次

### 基础能力

- 完成 Project Overlay 编辑和生效预览；
- Pack 安装、启用、固定 Revision、URL digest 校验；
- Creator Profile 查看、启用和明确作用域。

### 增强能力

- 自动 scope 链；
- 用户改稿候选的 confirm/edit/reject 完整出口；
- 正反例检索、冲突整理和跨书验证；
- 将 v0 的参考文章画像重新表达为可审查的 Profile 候选或 Pack 资料。

## 7. 验收标准

1. 用户可以说明某条规则来自哪里、作用于哪本书和哪个 Worker；
2. 同一 Profile 在第二本书生效前经过用户确认，且未确认候选不会改变 Prompt digest；
3. Pack 升级不会让已经运行的 Operation 偷偷换版本；
4. URL Pack 在解包和启用前验证预期 digest；
5. 用户可以拒绝、停用、编辑和回滚资产，不需要删除历史。
