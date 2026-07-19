# GORM Gen 生成流程

本项目采用 schema-first：表结构以 `deploy/mysql/init/001_schema.sql` 为准，运行时不使用 GORM AutoMigrate。

## 安装 gentool

```bash
./scripts/install-gentool.sh
```

默认安装 `gorm.io/gen/tools/gentool@v0.0.2`。`gentool` 是独立工具模块，不跟 `go.mod` 中的 `gorm.io/gen v0.3.28` 共用同一组版本号。

如需临时覆盖版本：

```bash
./scripts/install-gentool.sh v0.0.2
GORM_GEN_TOOL_VERSION=v0.0.2 ./scripts/install-gentool.sh
./scripts/install-gentool.sh 0.0.2
```

## 生成 query/model

先确保 MySQL 已经按 `deploy/mysql/init/001_schema.sql` 初始化：

```bash
docker compose up -d mysql
```

然后设置 DSN 并生成：

```bash
export JXH_GORMGEN_DSN="jxh:jxh_password@tcp(127.0.0.1:3306)/jxh_bot?charset=utf8mb4&parseTime=True&loc=Local"
make gormgen
```

`make gormgen` 会通过 `go generate ./internal/storage` 调用 `scripts/gormgen.sh`。生成参数固定在脚本里，不要手动拼临时 `gentool` 参数更新仓库代码。

当前生成表清单包括知识库、管理员/黑名单、定时任务、事件去重和群申请登记相关表。新增 MySQL 表时先更新 `deploy/mysql/init/001_schema.sql`，再同步 `scripts/gormgen.sh` 的 `-tables` 参数。

## Model ownership

当前阶段保留 `internal/storage/models.go` 作为 storage 对外返回类型和业务转换类型。

`internal/storage/model` 只作为 generated query 的内部实现类型。如果后续要完全切换 generated model，需要单独做一次行为等价迁移。
