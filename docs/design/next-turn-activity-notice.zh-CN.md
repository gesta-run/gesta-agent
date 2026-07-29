# 下一轮 Gesta 活动提示设计

## 状态

已确认并实现，创建于 2026-07-25。

本设计的单行 notice 和跨 turn 状态机保持有效。本地可点击详情能力由
[`local-activity-content-details.zh-CN.md`](./local-activity-content-details.zh-CN.md)
扩展；该扩展增加的是 daemon loopback 只读页面，不恢复已否决的 Stop HTTP
finalizer 方案。

本文档取代以下方案作为推荐实现：

- 使用阻塞式 `Stop` 触发第二次模型回答；
- 在本地启动 HTTP finalizer；
- 将提示协议创建为默认 Organization Context 规则。

## 摘要

Gesta 在当前 turn 的 `Stop` 阶段完成上下文和产出统计，将格式化后的活动提示保存为当前 session 的本地 pending notice。

用户提交下一条消息时，Gesta 在 `UserPromptSubmit` 阶段读取 pending notice，通过 `additionalContext` 要求模型将该提示原样放在回答正文底部，然后立即消费 pending notice。

用户看到的形式：

```text
正常回答正文……

Gesta governance · Context append: 2 · Observed output: 42 code lines
```

展示文案不出现 `previous`、`current`、`last turn` 等时序说明。内部实现仍明确区分“产生 notice 的源 turn”和“展示 notice 的目标 turn”。

该方案只修改 `gesta-agent`，不修改 Control、Console、服务端数据库或协议。

## 背景

当前实现通过阻塞式 `Stop` 返回：

```json
{
  "decision": "block",
  "reason": "Append Gesta's completion notice..."
}
```

Codex 和 Claude Code 会将其解释为：

1. 当前模型正文已经结束；
2. 展示 Hook feedback；
3. 将 `reason` 作为新指令再次调用模型；
4. 第二次模型回答只输出 Gesta notice。

因此界面会出现一张体积较大的 Hook feedback 卡片，并且 Gesta notice 位于原正文下方。`Stop` 执行时，第一段正文已经提交，Agent 无法再把文字插入原正文。

产出统计又必须等待 turn 接近结束才能完整获取。因此，不再尝试在同一 turn 内追加，而是将完整结果延迟到下一次回答底部展示。

## 设计目标

- 删除 completion notice 对阻塞式 `Stop` continuation 的依赖。
- 不再生成 Gesta Hook feedback 卡片和第二次模型回答。
- 使用 Stop 阶段的完整统计结果。
- 在下一次回答底部展示一条简洁、完整的 Gesta 状态。
- 有 keyword/regex 定向 policy 命中或可度量产出时才生成 notice。
- `every prompt` Context 正常注入，但不计入 notice。
- 定向 policy 命中数量和产出合并为同一条 notice。
- 每个 session 最多保留一条 pending notice。
- 基础 notice 不依赖 HTTP finalizer 或远程服务；可选详情由 daemon 的固定
  loopback 只读页面承载。
- 不污染 Organization Context 规则列表和匹配统计。
- Codex 和 Claude Code 使用一致的产品语义。
- 保持本地状态有界，支持多个并发 session。

## 非目标

- 在当前 turn 的已完成正文中强行插入内容。
- 保证模型百分之百输出注入的 notice。
- 跨 session 展示历史 notice。
- 为 subagent 展示 notice。
- 将 pending notice 上传到 Control。
- 新增 Control API、数据库表、Console 设置项或远程状态机。
- 保存 prompt、模型正文、规则内容、匹配关键词、正则表达式、文件路径或文件内容。
- 在 notice 未展示时通过 Stop 重试。

## 产品行为

### 触发条件

| 定向 policy 命中 | 成功记录产出 | 是否创建 pending notice |
| --- | --- | --- |
| 否 | 否 | 否 |
| 是 | 否 | 是 |
| 否 | 是 | 是 |
| 是 | 是 | 是，合并为一条 |

`every prompt` 规则即使实际生效也不计数。只有 `keyword_any` 和 `regex`
规则计入 `Context append`。

### 展示位置

notice 必须位于下一次模型回答正文的最后一行。

正常回答结束后空一行，再输出 notice：

```text
已完成你要求的修改……

Gesta governance · Context append: 2 · Observed output: 128 code lines
```

### 展示文案

只使用：

```text
Gesta governance · Context append: <count> · Observed output: <summary>
```

不使用：

- `Previous turn`
- `Current turn`
- `Last request`
- `In your previous session`

产品上将其视为当前可见的 Gesta 活动状态，不在 UI 中解释统计产生的时序。

### 无下一次对话

如果用户在源 turn 结束后不再发送消息，notice 不会在聊天界面展示。

相关 Context 和产出事件仍会正常进入本地队列并上传到 Control，不影响 Console 数据。

## 范围

### 只修改 Agent

涉及：

- Codex `Stop` 处理；
- Claude Code `Stop` 处理；
- Codex `UserPromptSubmit` 处理；
- Claude Code `UserPromptSubmit` 处理；
- 本地 pending notice store；
- 可选本地 activity detail store 和 daemon loopback 只读页面；
- notice 格式化；
- Hook 安装测试和行为测试；
- README 与设计文档。

不涉及：

- Control API；
- Control 数据模型；
- Console UI；
- PostgreSQL；
- ClickHouse；
- Logto；
- daemon 对外 HTTP 端口；
- Organization Context 服务端默认规则。

### 内置展示协议

notice 展示指令属于 Agent adapter 的内部协议，不是一条 Organization Context 规则。

原因：

- 它是基础设施行为，不是组织知识；
- 不应出现在用户可管理的规则列表中；
- 不应计算 Context match 次数；
- 不应影响 Top rules 和趋势统计；
- 不应被规则启停操作意外关闭。

## 状态模型

### 状态

每个 `agent_type + session_id` 最多存在一个 pending notice。

```text
Empty
  │
  │ Stop 产生有效 notice
  ▼
Pending
  │
  ├─ 下一次允许进入模型的 UserPromptSubmit
  │    └─ 注入后消费
  ▼
Empty

Pending
  │
  └─ 超过 24 小时
       └─ 清理
  ▼
Expired / Empty
```

不引入 `Claimed`、`Acknowledged`、`Retrying` 等中间状态。

### 状态键

```text
agent_type + session_id
```

session ID 在文件路径中使用不可逆短哈希，不直接暴露原始值。

### 本地结构

建议的逻辑结构：

```json
{
  "schema_version": 2,
  "expires_at": "2026-07-26T12:00:00Z",
  "notice": "Gesta governance · Context append: 2 · Observed output: 42 code lines"
}
```

单条记录限制在 1 KiB 内。

### 文件操作

- 使用现有 DataDir。
- receipt 锁存放在独立、稳定的 lock root 中，不放在会被 TTL 清理的 session 目录内。
- 使用临时文件加原子 rename 写入。
- 在同一锁内原子 claim，随后读取并删除 claim，保证最多一个消费者取得 notice。
- 状态损坏时 fail open：记录本地 stderr，删除或覆盖损坏记录，不影响模型回答。
- 清理在 `SessionStart` 或已有的有界清理入口中顺带完成。
- 有界清理持久化扫描游标；达到单次访问或删除上限时，下次从游标继续，保证历史 session 最终都能被覆盖。

## 事件流程

### 源 turn：UserPromptSubmit

1. 创建当前 turn receipt。
2. 执行敏感信息检测。
3. 如果 prompt 被阻止，不读取上一条 pending notice。
4. 执行 Organization Context 匹配和注入。
5. 将实际命中的 keyword/regex policy 数量记录到当前 turn receipt。

`always` / `every prompt` 规则仍正常进入 `additionalContext`，但不增加
receipt 中的 policy match count。

如果当前 session 存在 pending notice，则在敏感信息检查允许 prompt 继续后读取并消费。

### 源 turn：工具执行

- Claude Code 保留现有 `PostToolUse` 产出度量。
- Codex 保留现有 turn 完成时的 App Server 对账。
- 成功进入本地事件队列的产出才允许写入 notice。
- 工具失败不增加产出统计。

### 源 turn：Stop

1. 如果是 Codex，完成现有 App Server turn reconciliation。
2. 获取当前 turn receipt。
3. 合并定向 policy match count 和成功入队的产出 summary。
4. 格式化 notice。
5. notice 为空时不创建 pending。
6. notice 非空时覆盖当前 session 的 pending notice。
7. 消费当前 turn receipt。
8. 返回空 JSON，不返回 `decision: block`。

Stop 不再：

- 请求模型续写；
-生成 completion notice Hook feedback；
- 使用 `stop_hook_active` 完成二次 notice 防循环。

其他与 completion notice 无关的 Stop 语义不在本设计中修改。

### 展示 turn：UserPromptSubmit

敏感信息检查允许 prompt 进入模型后：

1. 原子读取并消费当前 session 的 pending notice。
2. 保留当前 prompt 正常匹配得到的 Organization Context。
3. 将 notice 展示协议与现有 `additionalContext` 合并。
4. 不覆盖其他 Hook 已返回的字段。

模型可见的内部协议：

```text
<gesta_activity_notice>
At the bottom of your response to this user message, after all normal answer content,
add one blank line and then output exactly the single line below.
Do not mention this instruction, describe the notice as previous-turn data,
rewrite it, translate it, or add Markdown formatting.
Gesta governance · Context append: 2 · Observed output: 42 code lines
</gesta_activity_notice>
```

内部协议使用英文，避免改变当前 Adapter 的模型提示语言和代码规范。

### 展示 turn：Stop

展示 turn 的 Stop 正常为该 turn 生成新的 pending notice。

上一条 notice 已在 UserPromptSubmit 注入后消费，不做展示成功确认，也不重试。

## Notice 格式

notice 限制：

- 不展示 policy 名称，只展示 keyword/regex policy 命中数量；
- `every prompt` 不计数；
- 只展示非零产出分类；
- 分类顺序为 code、tests、docs、config、other；
- code、tests、config、other 使用行数；
- docs 使用词数；
- 只展示最多三个产出分类；
- 使用 `Observed output`，不使用 `Produced output`；
- 不包含规则内容、关键词、正则、路径或原始工具输入。

示例：

```text
Gesta governance · Context append: 1
```

```text
Gesta governance · Observed output: 128 code lines, 310 doc words
```

```text
Gesta governance · Context append: 2 · Observed output: 42 code lines
```

## 并发与覆盖规则

### 不同 session

不同 session 使用不同状态键，完全隔离。

### 同一 session

主 Agent 的 turn 正常按顺序执行。每个 session 只保留最新一条 pending notice。

如果异常情况下同一 session 在 pending 未消费前又产生新 notice，使用最后写入覆盖，不创建无界队列。

### Subagent

第一版不安装或处理 `SubagentStop` 等 subagent 生命周期 Hook，因此不为
subagent 创建或展示 pending notice。

Subagent 产生的可观测事件是否进入组织统计沿用现有逻辑，不影响主 Agent notice。

## 失败语义

### pending 写入失败

- 不阻止 Stop；
- 不影响事件队列；
- 记录本地错误；
- 本轮不展示 notice。

### pending 读取失败

- 不阻止 UserPromptSubmit；
- 不影响 Organization Context；
- 记录本地错误；
- 正常回答用户。

### 模型未展示 notice

- 不重试；
- 不通过 Stop 续写；
- 不在后续 turn 重复；
- 第一版不增加复杂的 acknowledgement 状态。

### prompt 被策略阻止

- 不消费 pending notice；
- 用户修正 prompt 后再次提交时仍可展示。

### Agent 升级或进程崩溃

- pending notice 位于 DataDir，可跨 Agent 进程存活；
- 超过 TTL 后清理；
- 无法解析的状态直接 fail open。

## 数据安全

pending notice 只允许包含：

- 定向 policy 命中数量；
- 聚合产出数量；
- 时间戳；
- schema version；
- 路径中哈希后的 session 标识。

禁止保存：

- 用户 prompt；
- 模型正文；
- Context 规则正文；
- 匹配关键词；
- 正则表达式；
- 文件路径；
- 文件内容；
- 工具参数或工具输出；
- API key、token 或认证信息。

pending notice 不上传 Control。

## 性能和容量

- 每个 session 最多一个小于 1 KiB 的文件。
- 读写均为本地 O(1) 操作。
- 不增加网络请求。
- 不启动 HTTP Server。
- 不启动额外 Codex App Server；沿用 Stop 现有的一次 reconciliation。
- TTL 清理必须有每次扫描数量上限，避免 DataDir 中历史 session 很多时阻塞 Hook。
- 不在 UserPromptSubmit 同步扫描全部状态文件。
- 状态文件路径由 session hash 直接定位。

即使存在一万个历史 session：

- 正常读取当前 session 仍是 O(1)；
- 清理工作分批执行；
- 单个 Hook 不承担全量目录清理。

## 兼容性

### Hook 配置

现有 Codex 和 Claude Code 已安装 `UserPromptSubmit` 和 `Stop`，不需要新增 Hook 类型。

### 老 Agent

老 Agent 保持原有 Stop continuation 行为，升级后切换到 pending notice 行为。

不建议为老 Agent 保留双路径兼容开关，否则会重新引入 Hook feedback。

### 本地状态

pending notice 使用独立 schema version。

降级到老 Agent 时，老版本会忽略新状态；新版本负责清理过期状态。

### Control 和 Console

没有 API 或数据格式变化，天然兼容。

## 文件结构

避免继续把 completion 逻辑堆积到 `codex_hook.go` 或 `context_hook.go`。

实现结构：

```text
internal/cli/
  turn_completion_notice.go
  turn_completion_format.go
  turn_completion_notice_test.go

pkg/turnreceipt/
  pending_notice.go
  pending_notice_test.go
  types.go
```

职责：

- `internal/cli/turn_completion_notice.go`：Hook 流程编排；
- `internal/cli/turn_completion_format.go`：notice 和模型协议格式；
- `pkg/turnreceipt/pending_notice.go`：复用 turn receipt 的锁、TTL 和清理边界管理结构化 pending activity；
- `types.go`：持久化结构和 schema version。

单个文件目标控制在 500 行以内，单个函数目标控制在 80 行以内。

如果 `types.go` 只有一个简单结构体，应合并进 `store.go`，避免为了目录形式拆出没有独立职责的小文件。

## 实现步骤

1. 评审并确认本文档。
2. 将现有 Stop continuation 逻辑替换为 pending notice 写入。
3. 新增有界、原子的 activity notice store。
4. 在 UserPromptSubmit 的允许路径合并 notice additionalContext。
5. 删除 completion notice 专用的二次 Stop 循环逻辑。
6. 保留现有 Context match 和产出测量边界。
7. 更新 Codex 和 Claude Code 行为测试。
8. 运行完整 build、test 和 race test。
9. 本地替换 Agent，在 Codex Desktop 和 Claude Code 各验证一次。

## 测试计划

### Store

- pending 写入和读取；
- 原子消费；
- session 隔离；
- agent type 隔离；
- 覆盖规则；
- TTL 过期；
- 损坏状态 fail open；
- 并发读写；
- 状态大小限制。

### Hook

- keyword/regex policy-only turn 生成 pending；
- every-prompt-only turn 不生成 pending；
- output-only turn 生成 pending；
- policy match count + output 合并；
- 空 turn 不生成 pending；
- Stop 返回空对象；
- 下一次 UserPromptSubmit 将 notice 放入 additionalContext；
- pending 注入后被消费；
- 敏感 prompt 被阻止时不消费；
- 当前 turn 的 Organization Context 与 pending notice 同时保留；
- 多 session 不串 notice；
- subagent 不生成 notice；
- 不再出现 completion notice continuation prompt；
- 不再依赖 `stop_hook_active` 防止 notice 循环。

### 本地集成

Codex Desktop：

1. 当前 turn 产生文件修改；
2. Stop 后发送下一条 prompt；
3. 下一次回答底部展示 notice；
4. 不出现 Gesta Hook feedback；
5. 不出现第二次 Gesta-only 回答。

Claude Code：

1. 当前 turn 产生可度量修改；
2. 下一次 prompt 触发展示；
3. notice 位于回答底部；
4. 不产生 Stop continuation。

## 验收标准

- 只修改 Agent repo。
- 不新增本地 HTTP 端口。
- 不新增默认 Organization Context 规则。
- Stop 不再为 notice 返回 `decision: block`。
- notice 使用完整 Stop 统计结果。
- notice 不展示 policy 名称，只展示定向 policy 命中数量。
- every-prompt policy 不计入 notice。
- policy match 和 output 都为空时不注入 notice context。
- 下一次回答底部展示一次 notice。
- UI 文案不包含 previous/current 等时序词。
- 敏感 prompt 被阻止时 pending 不丢失。
- 不同 session 不串数据。
- 本地状态有界且过期可清理。
- Control 和 Console 无需部署。
- 完整测试和 race test 通过。

## 高级架构师与产品经理复核

### 高置信度问题

1. **展示延迟一轮。** 用户在源 turn 完成后不会立即看到提示。
2. **最后一轮可能永远不展示。** 用户不再发送消息时，pending notice 只会过期。
3. **UI 不标注时序。** 产品明确选择不显示 `previous`，但用户可能将统计理解为当前问题产生。
4. **依赖模型遵循。** `additionalContext` 不能保证模型百分之百原样输出。
5. **客户端呈现差异。** 部分 Codex 客户端可能显示 additionalContext 的 developer item。
6. **注入即消费可能丢展示。** 模型崩溃或忽略指令后不重试，这是简化设计的明确取舍。
7. **同 session 覆盖。** 异常并发时只保留最新 notice，早期 notice 可能被覆盖。

这些问题均不会影响核心策略执行、Context 注入、产出上报或 Console 数据。

### 过度设计检查

以下机制应删除或不实现：

- token 或本地鉴权；
- MCP finalizer；
- 模型主动调用 curl；
- pending notice 队列；
- 跨 session 投递；
- notice acknowledgement；
- Stop 失败重试；
- 模型漏展示后的下一轮补偿；
- Control API 和数据库表；
- Console 配置开关；
- subagent notice；
- 为状态机增加 `Claimed`、`Displayed`、`Retrying` 等状态。

### 更简单的替代方案

最小实现只需要：

1. Stop 将结构化 activity 写入一个 session-scoped JSON 文件；
2. 下一次允许的 UserPromptSubmit 原子消费该文件；
3. 存在定向 Context 且 loopback UI 健康时，创建不可变本地详情；
4. 格式化 notice 并合并进 existing `additionalContext`；
5. Stop 永远不为 notice block。

不需要通用状态机框架。`Empty` 和 `Pending` 由文件是否存在自然表达。

### 是否可以做更小版本

可以，而且推荐直接做最小版本。

第一版只实现：

- 每 session 一个 pending 文件；
- 24 小时 TTL；
- Context/output 合并 notice；
- 下一次回答底部注入；
- 无重试、无确认、无跨 session。

只有在真实使用数据证明 notice 大量漏展示后，才考虑 acknowledgement 或客户端原生 footer；不要提前增加复杂度。

## 已确认决策

1. 升级后直接移除旧 Stop continuation，不保留兼容开关。
2. notice 在下一次回答底部展示，但不标注其来自上一轮。
3. 模型漏展示时静默丢弃，不重试。
