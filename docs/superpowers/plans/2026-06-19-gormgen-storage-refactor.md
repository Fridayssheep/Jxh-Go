# GORM Gen Storage Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把当前“存在但未进入运行时代码路径”的 GORM Gen，收敛成 storage 模块内部可维护、可再生成、可测试的类型安全 DAO 方案。

**Architecture:** `internal/storage` 继续作为业务侧唯一持久化边界，bot、knowledge、commands、scheduler 不直接依赖生成代码。正式生成流程采用官方 `gentool`，从已经初始化好的 MySQL schema 生成 query/model，生成物只在 storage 包内部使用，外部 API 保持稳定。

**Tech Stack:** Go, GORM, GORM Gen, MySQL schema-first, `go generate`, storage integration tests.

---

## 1. 官方 GORM Gen 用法结论

参考资料：

- [GORM Gen Guides](https://gorm.io/gen/index.html)
- [GORM Gen Tool](https://gorm.io/gen/gen_tool.html)

官方文档给了两条路径：

1. **Go 配置式生成器**
   - 在 Go 代码里创建 `gen.NewGenerator(gen.Config{...})`。
   - 通过 `g.UseDB(gormdb)` 复用现有 GORM DB 连接。
   - 用 `g.ApplyBasic(...)` 或 `g.GenerateAllTable()` 生成类型安全 DAO。
   - 最后执行 `g.Execute()`。
   - 适合放进项目仓库，固定生成规则、复用项目配置、纳入代码审查。

2. **`gentool` 独立命令**
   - 安装：`./scripts/install-gentool.sh`，默认安装 `gorm.io/gen/tools/gentool@v0.0.2`。
   - 通过 `-dsn`、`-tables`、`-outPath`、`-fieldNullable`、`-fieldWithIndexTag`、`-fieldWithTypeTag`、`-fieldSignable` 等参数生成代码。
   - 适合 schema-first 项目从数据库反推 model/query。
   - 如果把参数固化在脚本或配置文件中，也可以作为本仓库正式生成流程。

本项目已经有 `cmd/gormgen/main.go`，它使用的是第一种方式。按本方案调整后，推荐把正式流程切到 `gentool`：减少项目自维护生成器代码，保留官方工具的默认行为，生成参数由脚本和文档固定。当前真正的问题不是选择哪种入口，而是“生成物没有成为 storage 的正式实现依赖”。

## 2. 当前仓库状态

相关文件：

- `cmd/gormgen/main.go`
  - 已经使用 `gorm.io/gen`。
  - 已经支持 `-config`、`-schema`、`-out`、`-apply-schema`。
  - 当前默认输出到 `internal/storage/query`。
  - 当前使用 `GenerateAllTable()` 从数据库表结构生成所有表。
  - 在采用 `gentool` 后会变成冗余工具，建议完成新流程后删除。

- `deploy/mysql/init/001_schema.sql`
  - 明确声明运行时不使用 AutoMigrate。
  - 表结构以 SQL schema 为准。
  - 注释里也写明 gorm/gen 从执行 schema 后的 MySQL 表结构生成 model/query。

- `internal/storage/models.go`
  - 手写了 `KnowledgeEntry`、`KnowledgeImportRun`、`Admin`、`Blacklist`、`ScheduledJob`、`ProcessedEvent`。

- `internal/storage/store.go`
  - 手写所有 CRUD、事务、转换逻辑。
  - 业务侧依赖 `storage.Store`，而不是依赖 generated query。

- `internal/storage/query`
  - 当前不存在。
  - `rg` 没有找到运行时代码导入 `internal/storage/query`。

判断：

- `gorm.io/gen` 依赖和 `cmd/gormgen` 当前处于“工具存在，但运行时未接入”的状态。
- `README.md` 说会生成到 `internal/storage/query`，但仓库里没有生成物，也没有任何代码使用它。
- 现有 storage 手写实现并不算错误；它承担了业务转换、向量状态保留、事件幂等等业务语义。重构时不能把这些业务规则下沉到裸 generated query 里。
- `gentool` 不会执行 `deploy/mysql/init/001_schema.sql`。它要求目标数据库已经建好表，所以正式流程需要先通过 Docker Compose 或 MySQL 初始化 schema，再运行生成命令。

## 3. 目标架构

目标依赖方向：

```text
cmd/bot
  -> internal/storage.Store
      -> internal/storage/query   generated, storage 私有实现细节
      -> internal/storage/model generated
      -> gorm.DB
```

非目标依赖方向：

```text
cmd/bot
internal/commands
internal/knowledge
internal/scheduler
  -> internal/storage/query
```

设计原则：

- `Store` 仍然是持久化模块对外 API。
- generated query 只替换 storage 内部重复、脆弱的 GORM 字符串查询。
- 业务语义继续留在 `Store`：
  - `UpsertKnowledgeEntries` 的 content hash 判断。
  - 内容不变时保留 `vector_status`、`vector_content_hash`、`vector_synced_at`。
  - 新导入批次后禁用未出现的旧知识。
  - `SeenOrMarkProcessedEvent` 的幂等判断。
  - scheduler view/domain object 转换。
- 不让 generated model 泄露到业务层，避免 schema 字段变化带动上层大面积重构。

## 4. 推荐方案

推荐使用 **`gentool` 作为唯一正式生成入口**。

理由：

- 官方文档明确提供 `gentool` 用于从数据库生成 structs/query。
- 当前项目是 schema-first，`deploy/mysql/init/001_schema.sql` 已经是表结构事实来源。
- 使用 `gentool` 可以删除 `cmd/gormgen` 里自维护的 SQL 分割、schema 执行、DSN 拼接逻辑。
- 生成参数通过脚本固定后，漂移风险可控。

推荐分两阶段做：

1. **先固定生成流程，不改 storage 行为**
   - 生成 `internal/storage/query`。
   - 明确 tables、输出路径、生成选项。
   - 增加 `scripts/gormgen.sh`，由脚本统一调用 `gentool`。
   - 把 generated code 纳入仓库。
   - 增加生成检查文档和 smoke test。

2. **再逐步把 Store 内部实现迁移到 generated query**
   - 先迁移只读查询。
   - 再迁移简单写入。
   - 最后迁移事务和 upsert 逻辑。

这样可以把风险控制在 storage 包内，不影响 bot 命令、RAG、scheduler、reload 等上层模块。

## 5. 文件结构规划

建议最终结构：

```text
scripts/install-gentool.sh
scripts/gormgen.sh
internal/storage/generate.go
internal/storage/models.go
internal/storage/store.go
internal/storage/query/          generated query package
internal/storage/model/          generated model package
internal/storage/store_test.go
docs/storage-gormgen.md
```

责任划分：

- `scripts/install-gentool.sh`
  - 安装固定版本的 `gentool`。
  - 默认版本是 `gorm.io/gen/tools/gentool@v0.0.2`。
  - 支持通过参数或 `GORM_GEN_TOOL_VERSION` 覆盖版本。

- `scripts/gormgen.sh`
  - 正式调用 `gentool`。
  - 从 `JXH_GORMGEN_DSN` 读取数据库连接。
  - 固定 tables、outPath、nullable/index/type/signable 等参数。

- `internal/storage/generate.go`
  - 放 `go:generate` 指令。
  - 让开发者在 storage 模块旁边看到生成入口。

- `cmd/gormgen/main.go`
  - 迁移完成后删除。
  - 删除前不再作为 README 推荐入口。

- `internal/storage/query`
  - 生成代码，不手写业务逻辑。
  - 生成后提交。

- `internal/storage/store.go`
  - 保持业务语义和模块边界。
  - 逐步从 `s.db.WithContext(ctx).Where(...)` 改为 generated query。

- `internal/storage/models.go`
  - 短期保留。
  - 如果后续决定完全使用 generated model，再单独做一次替换，不和 query 接入混在同一批改动里。

## 6. Task 1: 固定 gentool 生成入口

**Files:**

- Create: `scripts/install-gentool.sh`
- Create: `scripts/gormgen.sh`
- Create: `internal/storage/generate.go`
- Modify: `README.md`
- Modify: `docs/storage-gormgen.md`

- [ ] **Step 1: 安装 gentool**

Run:

```bash
./scripts/install-gentool.sh
```

Expected:

```text
gorm.io/gen/tools/gentool@v0.0.2 安装到 GOPATH/bin 或 GOBIN
```

可选指定版本：

```bash
./scripts/install-gentool.sh v0.0.2
GORM_GEN_TOOL_VERSION=v0.0.2 ./scripts/install-gentool.sh
./scripts/install-gentool.sh 0.0.2
```

- [ ] **Step 2: 新增 gentool 脚本**

Create `scripts/gormgen.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

if [[ -z "${JXH_GORMGEN_DSN:-}" ]]; then
  echo "JXH_GORMGEN_DSN is required" >&2
  echo "example: user:pwd@tcp(localhost:3306)/jxh_bot?charset=utf8mb4&parseTime=True&loc=Local" >&2
  exit 1
fi

gentool \
  -db mysql \
  -dsn "${JXH_GORMGEN_DSN}" \
  -tables "knowledge_entries,knowledge_import_runs,admins,blacklists,scheduled_jobs,processed_events" \
  -outPath "internal/storage/query" \
  -fieldNullable \
  -fieldWithIndexTag \
  -fieldWithTypeTag \
  -fieldSignable
```

- [ ] **Step 3: 新增 go generate 入口**

Create `internal/storage/generate.go`:

```go
package storage

//go:generate ../../scripts/gormgen.sh
```

- [ ] **Step 4: 初始化 MySQL schema**

Run:

```bash
docker compose up -d mysql
```

Expected:

```text
MySQL 容器启动，deploy/mysql/init/001_schema.sql 已在首次初始化数据卷时执行
```

- [ ] **Step 5: 设置生成 DSN**

Run:

```bash
export JXH_GORMGEN_DSN="user:pwd@tcp(localhost:3306)/jxh_bot?charset=utf8mb4&parseTime=True&loc=Local"
```

Expected:

```text
JXH_GORMGEN_DSN 指向已经包含目标表结构的 MySQL database
```

- [ ] **Step 6: 执行生成命令**

Run:

```bash
go generate ./internal/storage
```

Expected:

```text
internal/storage/query 目录被创建或更新
```

- [ ] **Step 7: 检查生成物**

Run:

```bash
rg -n "package query|type Query|func Use" internal/storage/query
```

Expected:

```text
能看到 generated query 包、Query 类型或 Use(db) 入口
```

- [ ] **Step 8: 提交第一批改动**

Run:

```bash
git add scripts/install-gentool.sh scripts/gormgen.sh internal/storage/generate.go internal/storage/query README.md docs/storage-gormgen.md
git commit -m "chore: add gentool storage generation workflow"
```

## 7. Task 2: 删除旧的项目内生成器

**Files:**

- Delete: `cmd/gormgen/main.go`
- Modify: `README.md`
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: 确认 generated query 已经提交**

Run:

```bash
test -d internal/storage/query
rg -n "package query|func Use" internal/storage/query
```

Expected:

```text
generated query 已存在
```

- [ ] **Step 2: 删除 `cmd/gormgen`**

Run:

```bash
rm -rf cmd/gormgen
```

Expected:

```text
项目内自维护生成器被移除，正式生成入口只剩 scripts/gormgen.sh + gentool
```

- [ ] **Step 3: 整理依赖**

Run:

```bash
go mod tidy
```

Expected:

```text
如果 generated query 仍 import gorm.io/gen，go.mod 会继续保留 gorm.io/gen；否则会被 tidy 移除
```

- [ ] **Step 4: 更新 README**

把旧命令：

```bash
go run ./cmd/gormgen -config config.yaml -schema deploy/mysql/init/001_schema.sql
```

替换为：

```bash
./scripts/install-gentool.sh
export JXH_GORMGEN_DSN="user:pwd@tcp(localhost:3306)/jxh_bot?charset=utf8mb4&parseTime=True&loc=Local"
go generate ./internal/storage
```

- [ ] **Step 5: 运行测试**

Run:

```bash
go test ./...
```

Expected:

```text
所有 package 测试通过
```

- [ ] **Step 6: 提交第二批改动**

Run:

```bash
git add -A cmd/gormgen README.md go.mod go.sum
git commit -m "chore: replace custom gorm generator with gentool"
```

## 8. Task 3: 给 Store 加行为回归测试

**Files:**

- Create or Modify: `internal/storage/store_test.go`

- [ ] **Step 1: 覆盖知识导入的关键业务语义**

测试目标：

- 同一个 `source_key` 二次导入时更新原记录。
- content 未变化时保留向量状态。
- content 变化时重置向量状态为 `pending`。
- 新 runID 导入后，旧 runID 且未出现的记录会被禁用。

Test shape:

```go
func TestStoreUpsertKnowledgeEntriesPreservesVectorStateWhenContentUnchanged(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	first := KnowledgeEntry{
		SourceKey:   "faq:one",
		Keyword:     "one",
		EntryType:   "knowledge",
		Answer:      "answer",
		Content:     "same content",
		Enabled:     true,
		ExactReply:  true,
		AIEnabled:   true,
		VectorStatus: VectorStatusReady,
	}

	require.NoError(t, store.UpsertKnowledgeEntries(ctx, []KnowledgeEntry{first}, 1))

	var stored KnowledgeEntry
	require.NoError(t, db.Where("source_key = ?", "faq:one").Take(&stored).Error)
	stored.VectorStatus = VectorStatusReady
	stored.VectorContentHash = stored.ContentHash
	now := time.Now()
	stored.VectorSyncedAt = &now
	require.NoError(t, db.Save(&stored).Error)

	second := first
	second.Answer = "new answer"
	require.NoError(t, store.UpsertKnowledgeEntries(ctx, []KnowledgeEntry{second}, 2))

	var got KnowledgeEntry
	require.NoError(t, db.Where("source_key = ?", "faq:one").Take(&got).Error)
	require.Equal(t, VectorStatusReady, got.VectorStatus)
	require.Equal(t, got.ContentHash, got.VectorContentHash)
	require.NotNil(t, got.VectorSyncedAt)
}
```

- [ ] **Step 2: 覆盖 processed event 幂等语义**

Test shape:

```go
func TestStoreSeenOrMarkProcessedEvent(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	seen, err := store.SeenOrMarkProcessedEvent(ctx, "group:message:1", time.Now())
	require.NoError(t, err)
	require.False(t, seen)

	seen, err = store.SeenOrMarkProcessedEvent(ctx, "group:message:1", time.Now())
	require.NoError(t, err)
	require.True(t, seen)
}
```

- [ ] **Step 3: 运行 storage 测试**

Run:

```bash
go test ./internal/storage
```

Expected:

```text
ok  	github.com/zjutjh/jxh-go/internal/storage
```

- [ ] **Step 4: 提交第三批改动**

Run:

```bash
git add internal/storage/store_test.go
git commit -m "test: cover storage store behavior"
```

## 9. Task 4: 只读查询迁移到 generated query

**Files:**

- Modify: `internal/storage/store.go`
- Test: `internal/storage/store_test.go`

- [ ] **Step 1: 扩展 Store 持有 generated query**

预期方向：

```go
type Store struct {
	db *gorm.DB
	q  *query.Query
}

func NewStore(db *gorm.DB) *Store {
	return &Store{db: db, q: query.Use(db)}
}
```

- [ ] **Step 2: 迁移 `ListEnabledKnowledge`**

把字符串查询：

```go
err := s.db.WithContext(ctx).Where("enabled = ?", true).Order("id ASC").Find(&entries).Error
```

迁移为 generated query 形式：

```go
ke := s.q.KnowledgeEntry
entries, err := ke.WithContext(ctx).
	Where(ke.Enabled.Is(true)).
	Order(ke.ID).
	Find()
```

如果 generated model 类型和当前 `storage.KnowledgeEntry` 不一致，在 `store.go` 内部增加转换函数，不把 generated model 暴露给调用方。

- [ ] **Step 3: 迁移 admin/blacklist/scheduler 的 list/is 查询**

迁移范围：

- `ListAdmins`
- `IsAdmin`
- `ListBlacklist`
- `IsBlacklisted`
- `ListScheduledJobs`
- `ListActiveSchedulerJobs`
- `HasProcessedEvent`

- [ ] **Step 4: 运行测试**

Run:

```bash
go test ./internal/storage ./cmd/bot
```

Expected:

```text
ok  	github.com/zjutjh/jxh-go/internal/storage
ok  	github.com/zjutjh/jxh-go/cmd/bot
```

- [ ] **Step 5: 提交第四批改动**

Run:

```bash
git add internal/storage/store.go internal/storage/store_test.go
git commit -m "refactor: use generated query for storage reads"
```

## 10. Task 5: 简单写入迁移到 generated query

**Files:**

- Modify: `internal/storage/store.go`
- Test: `internal/storage/store_test.go`

- [ ] **Step 1: 迁移简单 create/delete/update**

迁移范围：

- `AddAdmin`
- `RemoveAdmin`
- `ClearAdmins`
- `AddBlacklist`
- `RemoveBlacklist`
- `ClearBlacklist`
- `CleanupProcessedEvents`
- `AddScheduledJob`
- `RemoveScheduledJob`
- `MarkScheduledJobRan`

保留 GORM clause 的地方可以继续通过 generated query 的 underlying DB 或 `Clauses` 能力处理；不要为了“全量 generated query”牺牲清晰度。

- [ ] **Step 2: 运行测试**

Run:

```bash
go test ./internal/storage ./cmd/bot
```

Expected:

```text
ok  	github.com/zjutjh/jxh-go/internal/storage
ok  	github.com/zjutjh/jxh-go/cmd/bot
```

- [ ] **Step 3: 提交第五批改动**

Run:

```bash
git add internal/storage/store.go internal/storage/store_test.go
git commit -m "refactor: use generated query for simple storage writes"
```

## 11. Task 6: 事务和 Upsert 迁移

**Files:**

- Modify: `internal/storage/store.go`
- Test: `internal/storage/store_test.go`

- [ ] **Step 1: 迁移 `SeenOrMarkProcessedEvent`**

保持当前行为：

```text
第一次看到 event_key: 插入并返回 seen=false
第二次看到 event_key: 不重复处理并返回 seen=true
```

如果 generated query 的写法让 `OnConflict` 变复杂，保留局部 GORM 原生 `Clauses` 是可以接受的。这里的目标是减少散落字符串，不是消灭所有 GORM API。

- [ ] **Step 2: 迁移 `UpsertKnowledgeEntries`**

迁移时必须逐条保持：

- transaction 边界不变。
- `ContentHash` 每次由 `Content` 计算。
- 已存在且内容未变时保留向量状态。
- 已存在且内容变化时重置向量状态。
- 新记录补 `CreatedAt`、`UpdatedAt`。
- `runID != 0` 时禁用未出现在本批次的旧记录。

- [ ] **Step 3: 运行完整测试**

Run:

```bash
go test ./...
```

Expected:

```text
所有 package 测试通过
```

- [ ] **Step 4: 提交第六批改动**

Run:

```bash
git add internal/storage/store.go internal/storage/store_test.go
git commit -m "refactor: use generated query for storage transactions"
```

## 12. Task 7: 清理旧模型策略

**Files:**

- Modify: `internal/storage/models.go`
- Modify: `internal/storage/store.go`
- Modify: `docs/storage-gormgen.md`

- [ ] **Step 1: 决策模型来源**

二选一：

1. **保留手写 storage model**
   - 优点：业务字段命名稳定，迁移小。
   - 缺点：schema 和 model 仍可能漂移。
   - 适合当前阶段。

2. **完全切换 generated model**
   - 优点：schema-first 更彻底。
   - 缺点：nullable 字段、JSON 字段、时间字段类型可能引发较多转换。
   - 适合 generated query 稳定后单独做。

推荐当前阶段选择 1，先让 query 进入运行时代码路径，再评估是否删除手写 model。

- [ ] **Step 2: 记录最终决策**

在 `docs/storage-gormgen.md` 写明：

```markdown
## Model ownership

当前阶段保留 `internal/storage/models.go` 作为 storage 对外返回类型和业务转换类型。
`internal/storage/model` 只作为 generated query 的内部实现类型。
如果后续要完全切换 generated model，需要单独做一次行为等价迁移。
```

- [ ] **Step 3: 提交第七批改动**

Run:

```bash
git add internal/storage/models.go internal/storage/store.go docs/storage-gormgen.md
git commit -m "docs: record storage model ownership"
```

## 13. 验证策略

每个任务至少跑对应 package 测试。完成全部迁移前，不要删除手写 GORM 逻辑。

最终验证命令：

```bash
export JXH_GORMGEN_DSN="user:pwd@tcp(localhost:3306)/jxh_bot?charset=utf8mb4&parseTime=True&loc=Local"
go generate ./internal/storage
go test ./...
```

如果 CI 没有 MySQL，`go generate ./internal/storage` 不应放进默认 CI 流水线；可以单独做一个手动检查脚本或 Docker Compose 驱动的检查。

建议增加一个生成物漂移检查：

```bash
export JXH_GORMGEN_DSN="user:pwd@tcp(localhost:3306)/jxh_bot?charset=utf8mb4&parseTime=True&loc=Local"
go generate ./internal/storage
git diff --exit-code -- internal/storage/query
```

含义：

- 如果命令失败，说明生成物和当前 schema/config 不一致。
- 如果 CI 环境无法连接 MySQL，则不要启用这个检查。

## 14. 风险和处理方式

- **Nullable 字段类型变化**
  - `FieldNullable: true` 会让 nullable column 生成指针类型。
  - 处理方式：generated model 不直接暴露给业务层，统一在 `Store` 内转换。

- **JSON 字段**
  - `aliases_json`、`tags_json` 是 MySQL JSON。
  - 处理方式：短期保留当前 `string` + `jsonList/parseJSONList` 逻辑。

- **Unsigned ID**
  - 当前 schema 用 `bigint unsigned`。
  - 处理方式：保留 `FieldSignable: true`，并在测试中覆盖 `uint64` ID 行为。

- **生成物漂移**
  - 处理方式：固定 `scripts/gormgen.sh`，不要让开发者手敲临时 `gentool` 参数更新仓库。

- **gentool 依赖本机安装**
  - 处理方式：README 写明 `./scripts/install-gentool.sh`。
  - 默认固定安装 `gorm.io/gen/tools/gentool@v0.0.2`。
  - `gentool` 是独立工具模块，不跟 `gorm.io/gen` 共用同一组版本号；升级时先用 `go list -m -versions gorm.io/gen/tools/gentool` 确认可用版本。

- **gentool 不执行 schema**
  - 处理方式：生成前必须确认 MySQL 已由 `deploy/mysql/init/001_schema.sql` 初始化；如果 volume 已存在但 schema 变更未应用，需要重建测试库或手动执行 migration。

- **过度扩散 generated query**
  - 处理方式：只允许 `internal/storage` 使用 `internal/storage/query`。
  - review 时拒绝上层模块直接 import query 包。

## 15. 不做的事

- 不把 `query` 暴露成 bot/command/knowledge/scheduler 的直接依赖。
- 不在这次重构里改数据库 schema。
- 不把所有 GORM API 都替换掉；事务、OnConflict、复杂 upsert 可以保留局部原生 GORM。
- 不把 RAG、reload、scheduler runtime 和 storage query 迁移混在一个提交里。
- 不继续维护 `cmd/gormgen` 作为第二套正式生成入口。

## 16. 完成定义

满足以下条件才算完成：

- `gentool` 是唯一正式生成入口。
- `scripts/install-gentool.sh` 固定安装指定版本的 `gentool`。
- `scripts/gormgen.sh` 固定 tables、outPath 和字段生成参数。
- `internal/storage/query` 生成物存在并纳入版本控制。
- `cmd/gormgen` 已删除，或 README 明确不再推荐它。
- `Store` 内至少只读查询已经使用 generated query。
- storage 行为测试覆盖知识导入、管理员/黑名单、定时任务、事件幂等。
- `go test ./...` 通过。
- 文档说明如何安装固定版本 `gentool`、如何设置 `JXH_GORMGEN_DSN`、如何运行 `go generate`。

## 17. `gentool` 在本项目里的定位

`gentool` 是本项目正式生成入口，但不要让开发者手动拼临时参数更新仓库。正式命令应统一走：

```bash
./scripts/install-gentool.sh
export JXH_GORMGEN_DSN="user:pwd@tcp(localhost:3306)/jxh_bot?charset=utf8mb4&parseTime=True&loc=Local"
go generate ./internal/storage
```

允许在排查时直接运行脚本：

```bash
JXH_GORMGEN_DSN="user:pwd@tcp(localhost:3306)/jxh_bot?charset=utf8mb4&parseTime=True&loc=Local" ./scripts/gormgen.sh
```

不推荐直接运行裸 `gentool` 更新仓库代码；如果要临时对比输出，应写到 `/tmp`：

```bash
gentool \
  -db mysql \
  -dsn "user:pwd@tcp(localhost:3306)/jxh_bot?charset=utf8mb4&parseTime=True&loc=Local" \
  -tables "knowledge_entries,knowledge_import_runs,admins,blacklists,scheduled_jobs,processed_events" \
  -outPath /tmp/jxh-gormgen-query \
  -fieldNullable \
  -fieldWithIndexTag \
  -fieldWithTypeTag \
  -fieldSignable
```

## 18. Self-review

- Spec coverage:
  - 已覆盖官方 `gen.NewGenerator`、`UseDB`、`ApplyBasic`、`Execute` 的项目化用法。
  - 已覆盖 `gentool` 的安装、参数和在本项目中作为正式入口的定位。
  - 已覆盖当前仓库“有工具但未接入”的判断。
  - 已覆盖 storage 边界、迁移步骤、测试策略和风险。
  - 已按用户要求把推荐方案调整为 `gentool`。

- Placeholder scan:
  - 本文没有使用 `TBD`、`TODO` 或“后续补充”作为计划步骤。
  - 对每个代码修改任务都给出了目标文件、命令和期望结果。

- Type consistency:
  - 对外保留 `storage.Store`。
  - 生成物统一放在 `internal/storage/query`。
  - generated model 不直接暴露给业务层。
