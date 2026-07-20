# WPS 内存知识库与 ReAct Agent 重构计划

## 目标架构

```text
WPS（知识唯一真源）
  ├─ 启动：优先下载，失败读取上次有效 XLSX
  └─ /reload：只下载 WPS，失败保留当前状态
                  ↓
        atomic.Pointer[Index]
        ├─ entries []Entry
        └─ exact map[string]int
                  ↓
     普通关键词回复 + AI ReAct 搜索

MySQL
  ├─ knowledge_trigger_logs
  ├─ scheduled_jobs
  └─ group_join_requests
```

WPS 是知识库唯一真源。知识只存在于 WPS、本地最后一次有效的 XLSX
缓存和当前进程的只读内存索引中，不再复制到 MySQL。

## 1. MySQL 与生成代码

- 删除 `knowledge_entries`、`knowledge_import_runs`、`admins`、
  `blacklists`、`processed_events`。
- 删除相应 GORM model、query、DTO、转换函数和 Store 方法。
- 最终只保留 `knowledge_trigger_logs`、`scheduled_jobs`、
  `group_join_requests`，统一使用 `utf8mb4_0900_ai_ci`。
- 更新 `scripts/gormgen.sh`，从 MySQL 8.4 最终 schema 重新生成三张表的
  model/query。
- 不迁移旧数据。部署时重建 `data/mysql`，但保留
  `data/cache/knowledge.xlsx`；应用代码不自动删除数据目录。

`knowledge_trigger_logs` 结构：

```sql
CREATE TABLE `knowledge_trigger_logs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `source_key` varchar(255) NOT NULL,
  `trigger_type` varchar(32) NOT NULL,
  `group_id` bigint NOT NULL,
  `triggered_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_trigger_stats` (`triggered_at`, `source_key`, `trigger_type`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
```

日志不设置外键，不保存关键词、用户、消息、问题、事件键或检索分数。

## 2. WPS 生命周期

重构下载、解析、缓存和索引安装的职责：

- `WPSClient.Download` 只下载并验证 XLSX 文件签名，不提前覆盖缓存。
- 统一解析和验证流程：读取指定 sheet、执行 `ParseRows`、拒绝零条有效词条。
- 缓存通过同目录临时文件写入后 rename，避免破坏最后一份有效文件。

启动流程：

1. 尝试下载 WPS XLSX。
2. 解析并验证非空词条。
3. 完整构建新索引。
4. 原子保存 `data/cache/knowledge.xlsx`。
5. 原子安装新索引。
6. 任一步失败时记录 WPS 错误并读取本地缓存，重新解析、验证和构建索引。
7. WPS 与本地缓存都不可用时启动失败。

`share_url` 为空视为 WPS 不可用，可以回退本地缓存；无有效缓存则启动失败。
删除 `wps.sync_on_start`，启动固定执行上述流程。

`/reload` 流程：

1. 实时校验操作者为当前群 `owner` 或 `admin`。
2. 只从 WPS 下载，不用旧缓存冒充重载成功。
3. 下载、解析、非空验证、索引构建或缓存保存失败时返回失败。
4. 任一失败均保留当前索引和旧缓存。
5. 成功保存 XLSX 后一次性安装新索引。

## 3. 统一内存索引

不引入通用 cache 库。使用标准库 `atomic.Pointer[Index]` 整体替换不可变索引：

```go
type Index struct {
	entries []Entry
	exact   map[string]int
}
```

- `entries` 保存 WPS 顺序的完整词条，供 AI 搜索和低频统计展示使用。
- `exact` 将 trim 后的关键词和别名映射到 `entries` 下标，供普通关键词回复使用。
- 保持当前重复关键词或别名的后写覆盖语义，并增加测试锁定。
- 不增加 `searchDocuments`、`searchText` 或 `bySourceKey`。

`Entry.Content` 直接作为预计算搜索文本。构建时拼接 `keyword`、`aliases`、
`path`、`category` 和 `answer`，每部分执行 `cqreply.Parse(...).PlainText`，
再用 `strings.Fields` 合并空白并用 `strings.ToLower` 统一大小写。
`Content` 不包含 `source_key`、状态、类型或旧说明标签。

## 4. AI ReAct Agent

删除旧固定 RAG 链路：

- `RetrievalEngine`、`KnowledgeRetriever`、`RetrieverRef`。
- n-gram 打分、TopK、分数阈值和 AI TTL 缓存。
- `BuildPrompt` 和 `ExtractiveChat`。

使用已有 Eino `flow/agent/react.NewAgent` 和 `model.ToolCallingChatModel`，
不增加新的 Agent 框架。提供一个 `search_knowledge` 工具：

```json
{
  "query": "宿舍 空调",
  "mode": "and",
  "limit": 5
}
```

搜索语义：

- `and`：规范化后按空白分词，所有词都必须被 `Entry.Content` 包含。
- `or`：任一词被包含即可。
- `regex`：限制长度后使用 Go `regexp`，大小写不敏感。
- 只搜索 `Enabled && AIEnabled` 的词条。
- 结果保持 WPS 顺序，默认 5 条、最多 10 条，并限制单次返回总字符数。
- 非法正则作为结构化工具结果返回而不是 Go error，使 Agent 可以修改后重试。
- 工具返回 `source_key`、关键词、路径、分类和答案，不返回 `Content`。

Agent 约束：

- 回答前必须调用搜索工具，只能依据工具结果。
- 可以改写查询并重复搜索。
- 没有命中或依据不足时，由模型如实说明知识库暂时没有足够信息，不使用自身知识猜测。
- `MaxStep` 固定为 6，整个 `/ai` 使用 `ai.timeout_sec` deadline。
- OpenAI/Ark 初始化时绑定工具；绑定失败时 AI 初始化失败。
- 配置不完整或模型不支持 Tool Calling 时禁用 `/ai`，不再提供抽取式假 AI。

请求上下文收集工具实际返回的 `source_key`，同一次 `/ai` 内去重，供统计写入。

当前 NapCat 同步消费事件。仅将 `/ai` 异步执行，并使用固定容量 2 的
buffered channel semaphore；无空位时立即回复繁忙。不增加队列或配置项，
其他事件保持同步。

## 5. 统计迁移到 MySQL

- 保留 `triggerstats.Service` 的导出和时间窗口逻辑。
- 删除 Redis Store、事件哈希、触发文本、用户、消息和分数。
- 普通关键词或别名命中时写一条 `keyword_reply`。
- Agent 完成时，将本次请求去重后的来源批量写为 `ai_retrieval`。
- 无命中不写，统计失败只记录错误，不改变业务回复。

汇总按可选时间范围过滤，按 `source_key, trigger_type` 分组，使用
`COUNT(*)`、`MAX(triggered_at)`，按次数降序并提供稳定次序。
展示关键词时低频遍历当前 `Index.entries`；找不到时显示 `source_key`。

不增加统计聚合表、分区、保留清理任务或 Redis。

## 6. 管理员与黑名单

- 删除 `admins` 表、手动管理员授权、对应命令、帮助、Store 和测试。
- 删除 `blacklists` 表、全部黑名单命令和消息入口查询。
- `/admin` 和 `/reload` 每次通过 NapCat `GroupMemberRoleResolver` 查询当前群角色。
- 仅 `owner`、`admin` 放行；`member`、非法角色或查询失败均拒绝。
- 不缓存或持久化 QQ 群角色。
- 保留 `/admin ban`、restart、定时任务、群申请和词条统计。
- 收窄 `AdminHandler` 的 Store 接口，只保留定时任务所需方法。

## 7. NapCat 事件去重

- 删除 `processed_events`、`persistentDedupe`、进程内事件去重 map、清理协程、
  `event_dedupe` 配置、`napcat.Server.Dedupe` 和对应生命周期测试。
- NapCat consumer 直接调用 `handleEvent`。
- 保留 `group_join_requests.request_key` 唯一约束和
  `scheduled_jobs.last_run_at`；它们属于具体业务幂等。

## 8. Redis、配置和文档

删除 Redis Compose 服务、依赖关系、环境变量、数据挂载、配置结构、
`go-redis`、`miniredis`、Redis Store 和测试。

AI 最终只保留：

```yaml
ai:
  enabled:
  provider:
  base_url:
  api_key:
  model:
  timeout_sec:
  max_question_chars:
```

WPS 最终保留：

```yaml
wps:
  share_url:
  sid:
  cache_file:
  timeout_sec:
  sheet:
```

删除 `wps.sync_on_start`、`ai.top_k`、`ai.score_threshold`、`cache`、
`redis` 和 `event_dedupe`。同步更新 `config.example.yaml`、`.env.example`、
`docker-compose.yaml`、README、帮助文本、环境变量、`go.mod/go.sum` 和
`scripts/gormgen.sh`。历史 `docs/superpowers` 设计记录保持不变。

## 9. 实施顺序与验收

实施按可独立验证的逻辑阶段提交：

1. 写入并提交本计划。
2. 重构 WPS 生命周期和统一内存索引。
3. 删除旧 MySQL/Redis/权限/去重并接入 MySQL 统计。
4. 用 Eino ReAct Agent 替换固定 RAG。
5. 更新配置、文档和生成代码，完成全量验证。
6. 使用干净上下文的独立代理审查代码，修正后复审，直到无异议。

测试必须覆盖：

- WPS 正常启动、失败回退缓存、无效或零词条不替换旧状态。
- `/reload` 成功与失败保持。
- 精确关键词、别名和冲突覆盖语义。
- `Content` 字段组合和 CQ 清理。
- AND、OR、regex、非法 regex、limit、状态过滤和稳定顺序。
- Agent 强制搜索、重试、超时、MaxStep 和来源去重。
- `/ai` semaphore 满载。
- 群主、群管理员和普通成员权限。
- MySQL 日志写入和汇总。
- Redis、旧表、旧 RAG 和旧配置不存在运行时引用。

最终运行：

```text
go test ./...
go vet ./...
go build ./...
go mod tidy -diff
git diff --check
```

还需使用 MySQL 8.4 实际初始化最终 schema 并重新运行 gorm/gen。

## 非目标

- 不迁移旧数据。
- 不做 Web 搜索，不允许模型执行 SQL。
- 不做 BM25、embedding 或向量数据库。
- 不引入通用 cache 库。
- 不做统计聚合表、分区或多实例共享索引。
