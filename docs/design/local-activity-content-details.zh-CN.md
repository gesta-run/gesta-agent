# 本地活动详情：实际 Context 内容与 Gesta 品牌修订

## 状态

已批准并按方案 A 实现，创建于 2026-07-26。

本文是 `local-activity-details.zh-CN.md` 的第二版修订，覆盖第一版“不保存和展示
Context 内容”的产品决策。第一版的 loopback HTTP Server、24 小时 TTL、固定端口、
有界文件存储和 notice 行为保持不变。

## 目标

用户点击 Gesta notice 中的 `Details` 后，不仅能确认命中了哪些定向 Organization
Context rules，还能确认每条规则当时**实际 append 给模型的完整文本**。

页面同时使用 Console 的正式 Gesta Logo 和视觉语言，让本地页看起来像 Gesta
Console 的独立详情页，而不是另一套产品。

## 产品行为

### Context 展示

每条定向规则展示：

- 规则名称；
- 匹配类型；
- 优先级；
- 当次实际 append 的 Context 文本快照。

使用原生 `<details>`：

- 第一条规则默认展开；
- 其他规则默认收起；
- 展开区标题固定为 `APPENDED CONTENT`；
- 使用等宽字体和 `white-space: pre-wrap` 保留换行；
- 动态文本由 `html/template` 自动转义；
- 不使用 JavaScript、编辑器、搜索、Diff 或语法高亮。

```text
Gesta                                                   Local only
GOVERNANCE / LOCAL ACTIVITY
Activity detail

Codex · Captured Jul 26, 2026, 18:42:10 · Retention 24h

Applied context · 2
┌─────────────────────────────────────────────────────────────────────┐
│ ● Review Standards                         Keyword match · P90   ∧ │
│                                                                     │
│   APPENDED CONTENT                                                  │
│   Review the complete diff before finishing.                        │
│   Confirm tests cover the changed behavior.                         │
├─────────────────────────────────────────────────────────────────────┤
│ ● Deletion Operations                         Regex match · P80   > │
└─────────────────────────────────────────────────────────────────────┘

Observed output
42 code lines · 18 test lines

Stored only on this machine · Expires in 24 hours
```

`every prompt` 保持现有口径：

- 不计入 `Context append` 数量；
- 不进入本地详情；
- 页面只展示 notice 所代表的 targeted keyword/regex rules。

### Logo

正式 Logo 来源：

```text
console/public/brand/gesta-lockup-horizontal.svg
```

实现时通过 Go `embed.FS` 打包 HTML、CSS 和该 SVG，并在渲染时将可信的内置
CSS、SVG 原样内联到响应中：

- 不新增静态文件路由；
- 不加载远程图片；
- 不在运行时依赖 Console 仓库；
- 不在运行时读取磁盘模板或静态文件；
- 保留来源注释，品牌升级时显式同步；
- 不增加生成脚本或跨仓库构建依赖。

## 数据语义

页面必须展示 match-time snapshot，不能在用户打开页面时重新读取当前规则。

原因：

1. 规则可能在活动发生后被修改；
2. 当前规则内容不一定等于当时实际注入内容；
3. Details 的用途是解释一次已经发生的行为，而不是展示规则当前状态。

Matcher 已经在注入前完成：

- status 和 agent type 过滤；
- keyword/regex 匹配；
- priority 排序；
- 规则数量上限；
- Context 总长度上限；
- `TrimSpace`；
- 规则间使用两个换行拼接。

因此 Hook 应直接从同一个 match result 保存每条已选中规则的 trimmed content，不能
重新运行 matcher 或读取第二份 cache。

## 数据模型

### Turn receipt 与 pending notice

```go
type ContextRuleMatch struct {
    RuleID    string `json:"rule_id"`
    Name      string `json:"name"`
    MatchType string `json:"match_type"`
    Priority  int    `json:"priority"`
    Content   string `json:"content"`
}
```

约束：

- 最多 10 条；
- 只接受 `keyword_any` 和 `regex`；
- 忽略 `always`；
- `RuleID` 最大 128 bytes；
- `Name` 最大 160 bytes；
- 所有 `Content` 合计最多 8,000 runes，与 matcher 的
  `MaxContextContent` 一致；
- 不保存 prompt、keywords 或 regex pattern；
- Content 只进入本地 receipt、pending 和 activity detail，不进入事件上报 payload。

### Activity detail

```go
type Detail struct {
    SchemaVersion  int                `json:"schema_version"`
    ActivityID     string             `json:"activity_id"`
    CreatedAt      time.Time          `json:"created_at"`
    ExpiresAt      time.Time          `json:"expires_at"`
    AgentType      string             `json:"agent_type"`
    ContextMatches []ContextRuleMatch `json:"context_matches"`
    Output         OutputSummary      `json:"output"`
}
```

推荐新路径：

```text
<data_dir>/activity-details/v2/<activity_id>.json
```

## 兼容性

已确认采用方案 A，不兼容旧本地 schema。

### A：不兼容旧本地 schema（推荐）

- receipt、pending 和 activity detail 直接升级 schema；
- activity detail 目录从 `v1` 切换到 `v2`；
- 升级前生成的 Details 链接返回 unavailable；
- 旧文件由现有有界清理回收；
- 不实现迁移、双写或双读。

优点：实现和测试最简单，不保留只显示半套信息的历史页面。

### B：兼容旧 activity detail

- 新 reader 同时读取 v1 和 v2；
- v1 页面只能显示规则元数据，并标记 `Appended content unavailable`；
- receipt 和 pending 仍然不做兼容。

缺点：长期保留两个 reader、两种页面状态和额外测试，只换取最多 24 小时的旧链接
可用性。

当前仍处于开发阶段，因此推荐 A。

## 容量与性能

第一版记录只有元数据，单文件上限为 8 KiB。新增 Content 后，该上限不足以覆盖
matcher 允许的 8,000 runes。

修订后的硬上限：

| 项目 | 上限 |
| --- | --- |
| 每轮 targeted rules | 10 |
| 每轮 Context 内容 | 8,000 runes |
| receipt | 64 KiB |
| pending notice record | 64 KiB |
| activity detail record | 64 KiB |
| activity detail 数量 | 256 |
| TTL | 24 小时 |
| 最坏磁盘预算 | 16 MiB |

性能特征：

- 每轮最多写一个有界 JSON 文件；
- activity ID 直接映射文件名，读取为 O(1)；
- HTML 最多渲染 10 条规则和 8,000 runes；
- 不查询 Control、不访问数据库、不扫描历史记录；
- 现有清理访问上限、删除上限和持久化游标保持不变；
- 数据量达到上限后仍由数量和 TTL 双重限制，不随运行时间无限增长。

如果 JSON 编码后超过 64 KiB：

- 本轮 Context 仍正常注入模型；
- activity detail 创建失败；
- notice 降级为无 `Details` 链接；
- Hook 和 daemon 主流程继续运行。

## 隐私与安全

允许保存在本机：

- 规则 ID、名称、匹配类型和优先级；
- 当次实际 append 的 targeted Context 文本；
- Agent 类型、创建时间、过期时间；
- 聚合 output 数量。

禁止保存：

- prompt；
- assistant response；
- keywords；
- regex pattern；
- 文件名、路径和内容；
- tool arguments；
- API key、Control token；
- email、用户名、客户或组织 ID；
- session ID 和 turn ID。

现有保护保持：

- 只绑定 `127.0.0.1:3333`；
- Host allowlist；
- `Cache-Control: no-store`；
- 禁止外部资源和脚本的 CSP；
- `html/template` 自动转义；
- 文件 `0600`、目录 `0700`；
- TTL 24 小时；
- 页面只读，无表单和写操作。

## 实现步骤

1. 为 `ContextRuleMatch` 增加有界 `Content`；
2. 从同一个 `contextmatch.Result` 保存 match-time content；
3. 升级 receipt、pending 和 activity detail schema；
4. 将相关单记录上限调整为 64 KiB；
5. 在模板内联 Console 正式 Gesta Logo；
6. 按方案 A 重构本地页面；
7. 使用原生 `<details>` 展示每条规则的 Content；
8. 更新 Store、Hook、HTTP、安全、容量和并发测试；
9. 运行 `make verify`、`go vet ./...` 和 `go test -race ./...`；
10. 本地构建并替换 Agent，完成真实 Codex/Claude Code 验证。

## 测试重点

### 数据一致性

- 页面内容与当时 `AdditionalContext` 中对应规则内容一致；
- 规则修改后，旧 Details 仍展示旧快照；
- 多规则排序与 matcher 注入顺序一致；
- `always` 不保存；
- Unicode、换行、HTML 字符正确保存和转义；
- 8,000 runes 边界；
- 超出 64 KiB 时 fail open。

### 页面

- 使用正式 Gesta Logo；
- 第一条规则默认展开；
- 其他规则可用键盘展开；
- Content 保留换行；
- Context 中的 HTML 不会被执行；
- 页面无远程资源、脚本、表单和伪交互；
- 手机宽度下规则标题、Content 和 output 正确换行；
- unavailable 页面保持同一品牌风格。

### 回归

- notice 仍只注入一次；
- output-only notice 不生成 Details；
- local server 失败不影响 Hook；
- 多 session 不覆盖；
- 清理保持有界；
- Control 事件 payload 不包含 Content。

## 架构与产品复核

### 高置信问题

1. 不能事后读取当前规则内容，必须保存 match-time snapshot，否则 Details 会说谎。
2. 原 8 KiB/32 KiB 上限无法承载 matcher 允许的最坏输入，必须同步调整。
3. Content 属于组织治理文本，只能留在本机；不能复用服务端
   `context_rule.matched` 事件传输它。
4. 页面必须明确 `Local only` 和 TTL，避免用户误以为这是 Console 云端审计记录。

### 可以删除的过度设计

- 不需要规则历史、Diff、搜索、编辑器或语法高亮；
- 不需要 Copy toast 或客户端 JavaScript；
- 不需要 Control API、数据库表或 Console 页面；
- 不需要 Logo 静态资源路由或跨仓库运行时依赖；
- 开发阶段不需要为了最多 24 小时的旧链接维护双 schema reader。

### 更小版本是否可行

可行。最小完整版本只有：

1. 本地记录增加 `Content` 快照；
2. 本地记录上限调整为 64 KiB；
3. 页面内联 Logo，并用原生 `<details>` 显示 Content。

该版本已经完整解决“Gesta 实际 append 了什么”，其余能力都应暂缓。

## Review 决策点

实现前只需确认：

1. 是否采用推荐方案 A，不兼容旧本地 schema；
2. 是否接受 24 小时、256 条、最坏 16 MiB 的本地存储上限；
3. 是否确认 `every prompt` 继续不计数、不展示。
