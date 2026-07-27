# Organization Context 用户意图匹配边界设计

状态：Draft，待评审

## 1. 背景

当前 Gesta Agent 在 `UserPromptSubmit` 阶段直接使用 Hook 事件中的完整 `prompt` 进行 Organization Context 匹配。

对于 Codex，完整 `prompt` 除了用户实际输入，还可能包含客户端生成的包装信息，例如：

```text
# Files mentioned by the user:

## screenshot.png: /tmp/screenshot.png

## My request for Codex:

这个居中，上下左右，修复下

<image name=[Image #1] path="/tmp/screenshot.png">
</image>
```

因此，规则中的关键词可能命中文件名、本地路径、固定标题或图片元数据，而不是命中用户真正表达的请求。这会造成：

- 错误注入 Organization Context；
- 错误增加规则命中统计；
- 完成提示中的 `Context append` 数量失真；
- 规则所有者难以判断关键词是否真正覆盖了用户意图。

## 2. 目标

1. Organization Context 的关键词和正则规则只匹配用户实际请求正文。
2. `always` 规则保持现有语义：只要存在有效用户请求即可注入。
3. 敏感信息检测继续检查完整原始输入，不因正文提取而缩小防护范围。
4. Codex 与 Claude Code 使用各自明确的输入提取器，不把客户端格式判断混入规则匹配器。
5. 不改变 Control API、规则数据结构和本地缓存格式。
6. 不记录或上传用户原始输入、提取后的正文、附件路径或图片元数据。

## 3. 非目标

- 本次不修改 `keyword_any` 的大小写不敏感子串语义。
- 本次不增加单词边界、分词、模糊匹配或语义匹配。
- 本次不读取附件内容，不进行 OCR，也不对图片内容做匹配。
- 本次不增加规则级别的“是否匹配附件”配置。
- 本次不修改 Control 或 Console。

## 4. 核心设计

新增独立的 `promptscope` 包，将 Hook 原始输入转换为供 Organization Context 使用的匹配文本：

```go
func Extract(agentType, rawPrompt string) string
```

解析器内部区分普通输入、有效 Codex 包装和损坏 Codex 包装，但不把解析状态或附件路径暴露给调用方。附件路径只在单次函数调用内用于识别客户端生成的图片块，避免不必要的数据生命周期和公开 API。

处理流程调整为：

```text
Hook raw prompt
    ├── Sensitive Data 检测：raw prompt
    └── promptscope.Extract(agentType, raw prompt)
            └── Organization Context 匹配：extracted text
                    ├── 规则命中事件
                    ├── Context 注入
                    └── Turn 内命中记录
```

Pending activity notice 的状态机和注入顺序保持不变。

## 5. Codex 输入提取规则

### 5.1 识别完整包装，而不是搜索单个标记

只有同时满足以下条件时，输入才被视为 Codex 文件包装格式：

1. 存在独立成行的 `Files mentioned by the user:` 标题；
2. 该标题之后存在至少一个可解析的附件路径条目；
3. 附件区域之后存在独立成行的 `My request for Codex:` 标题；
4. 标题顺序正确；请求正文允许为空。

Markdown 标题前缀允许为一个或多个 `#`，但标题文本必须完整匹配。匹配前统一处理 `LF` 与 `CRLF`。

解析器使用三态分类：

- `direct`：没有任何 Codex 包装标记，返回完整原始输入；
- `envelope`：包装结构和附件区域有效，返回提取后的请求正文；
- `malformed_envelope`：出现一个或多个包装标记，但结构、顺序或附件区域无效，返回空字符串。

三态只在解析器内部使用。这样普通 `hello` 仍按直接输入匹配，而疑似损坏的包装不会回退匹配文件名和路径。

### 5.2 用户正文范围

`UserIntent` 从 `My request for Codex:` 标题后的第一个非空字符开始，到输入末尾结束。

随后仅删除由 Codex 客户端生成的尾部图片引用块。一个图片块必须同时满足：

- 是完整的 `<image ...>...</image>` 尾部块；
- 其 `path` 属性与前置附件列表中的某个路径完全一致；
- 删除后只留下空白或另一个满足条件的图片块。

这样可以保留用户主动输入的 XML、HTML 示例，以及正文中间的图片相关描述。

### 5.3 空正文

若包装格式有效，但请求正文为空，或者去除生成的图片引用后正文为空：

- `UserIntent` 为空；
- 不执行 targeted 规则匹配；
- 不注入 `always` 规则；
- 不记录 Organization Context 命中。

附件本身不应被视为用户请求。

### 5.4 损坏包装

若输入包含 `Files mentioned by the user:` 或 `My request for Codex:` 包装标记，但缺少另一个标记、路径格式异常或标记顺序错误，提取器将其识别为损坏包装并返回空字符串。

损坏包装不执行 targeted 或 `always` 规则匹配，也不记录 Organization Context 命中。静默跳过比使用可能包含客户端元数据的文本更安全。

完全不包含已知包装标记的输入仍视为普通直接输入，因此 `hello`、旧客户端的普通文本以及其他 Agent 的直接输入不会受到影响。

## 6. Claude Code 行为

当前 Claude Code Hook 的 `prompt` 是直接用户输入，没有发现与 Codex 相同的文件包装协议。

因此：

```text
agentType = claude_code
extracted text = rawPrompt
```

以后若 Claude Code 增加稳定的包装格式，应新增 Claude 专属提取器，而不是在通用规则匹配器中增加条件判断。

## 7. 敏感信息检测边界

敏感信息检测必须继续使用完整 `rawPrompt`，原因包括：

- 本地附件路径可能包含用户名、项目名或凭据片段；
- 客户端包装元数据仍属于将要发送给模型的输入；
- Organization Context 精确匹配与数据防泄漏的风险边界不同。

执行顺序保持为：

1. 检查完整原始输入；
2. 若策略阻断，立即返回；
3. 提取用户正文；
4. 匹配并注入 Organization Context；
5. 按现有逻辑处理 pending notice。

## 8. 规则语义

`contextmatch.Match` 不再了解 Codex 或 Claude Code 的包装格式，只接收已经限定作用域的文本。

现有规则语义保持不变：

- `always`：对非空 `UserIntent` 生效；
- `keyword_any`：大小写不敏感的子串匹配；
- `regex`：使用现有正则表达式匹配；
- 规则按优先级降序、Rule ID 升序排序；
- 最多注入 10 条规则；
- 总注入内容上限保持 8000 Unicode 字符。

## 9. 隐私与可观测性

继续只记录以下命中元数据：

- Rule ID；
- Rule name；
- Match type；
- Bundle version；
- 是否因注入上限被截断。

不记录：

- 原始 prompt；
- `UserIntent`；
- 附件路径；
- 命中的关键词；
- 正则命中的文本片段。

本次不新增服务器事件字段，因此不需要数据库迁移。

## 10. 性能与规模

### 10.1 输入提取

- 解析复杂度为 `O(n)`，其中 `n` 为 prompt 长度；
- 使用行偏移和字符串切片，避免多次复制完整 prompt；
- 不使用可能产生灾难性回溯的正则表达式；
- 附件列表是函数内局部数据，只保存路径引用，并设置合理的条目上限，防止异常输入造成无界增长；
- 尾部图片块从末尾向前识别，不扫描或重写正文中间内容。

### 10.2 规则匹配

本次不改变规则匹配复杂度。当前每轮排序规则和编译正则的成本可在后续独立优化为“缓存加载时构建不可变 matcher snapshot”，避免把作用域修复与匹配引擎重构耦合在同一个变更中。

### 10.3 并发

`promptscope.Extract` 是无状态纯函数，不引入锁、共享缓存或新的后台任务，多个 Codex/Claude Code 会话可安全并行调用。

## 11. 文件组织

计划新增：

```text
pkg/promptscope/
├── extractor.go
├── codex.go
└── extractor_test.go
```

计划修改：

```text
cmd/app/codex_hook.go
cmd/app/context_hook.go
```

`codex_hook.go` 负责在敏感信息检测之后调用 `promptscope.Extract`；`context_hook.go` 只使用提取后的文本调用现有 matcher。

## 12. 测试方案

单元测试至少覆盖：

1. 关键词只出现在文件名中，不命中。
2. 关键词只出现在本地路径中，不命中。
3. 关键词只出现在固定标题中，不命中。
4. 关键词出现在 `My request for Codex:` 后的正文中，正常命中。
5. 多个附件与多个尾部图片块可正确剥离。
6. 用户正文中主动编写的 `<image>` 示例不会被误删。
7. 仅出现一个包装标记时视为损坏包装，不匹配该输入。
8. 标记顺序错误或附件区域无效时视为损坏包装。
9. `CRLF`、中文正文和 Unicode 路径正常处理。
10. 有效包装但正文为空时不注入任何规则。
11. Claude Code 直接输入行为保持不变。
12. 敏感信息只出现在包装元数据中时，仍会被敏感信息检测发现。
13. pending activity notice 的合并和消费行为保持不变。
14. 命中事件和完成提示只统计正文触发的 targeted 规则。

实现完成后运行仓库完整 build、test 和现有 verify 流程。

## 13. 兼容性

该方案不修改服务端、API、缓存文件或规则数据结构，旧版 Agent 与新版 Control 可继续互操作。

唯一有意改变的行为是：新版 Agent 不再允许已识别 Codex 包装中的文件名、路径和生成图片元数据触发 Organization Context。

兼容策略采用三态而不是统一回退：

- 不含包装标记的普通输入继续匹配完整文本；
- 有效 Codex 包装只匹配请求正文；
- 包含包装标记但无法安全解析的输入不进行 Organization Context 匹配。

该策略既保证 `hello` 等普通输入正常工作，也不会在 Codex 包装格式损坏时重新引入元数据误命中。若未来客户端提供结构化的用户正文，应优先使用结构化字段替代文本包装解析。

## 14. 验收标准

- 截图所示输入中，`My request for Codex:` 之前的内容不会触发 targeted 规则。
- 用户实际请求正文中的关键词和正则仍能触发规则。
- `always`、规则优先级、注入数量和长度限制不变。
- 敏感信息扫描覆盖完整输入。
- 不新增 prompt 内容持久化或上传。
- Codex 与 Claude Code 的现有 Hook、活动统计和 pending notice 流程测试通过。
