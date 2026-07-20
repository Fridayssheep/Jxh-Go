<p align="center">
  <h1 align="center">精小弘 Jxh-Go</h1>
  <p align="center">基于 Go、NapCat 和 Eino 的精弘 QQ 群助手</p>
</p>

<p align="center">
  <a href="https://github.com/cloudwego/eino"><img alt="Eino" src="https://img.shields.io/badge/Eino-Agent-blue?style=flat-square"></a>
  <a href="https://github.com/NapNeko/NapCatQQ"><img alt="NapCat" src="https://img.shields.io/badge/NapCat-OneBot11-green?style=flat-square"></a>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat-square&logo=go">
  <img alt="MySQL" src="https://img.shields.io/badge/MySQL-8.4+-4479A1?style=flat-square&logo=mysql&logoColor=white">
</p>

## 简介

Jxh-Go 是精弘 QQ 群助手的 Go 重构版本，面向浙江工业大学相关 QQ 群的自动问答、知识库回复和群管理场景。

它通过 NapCat 接入 OneBot 11，以 WPS 回复表作为知识唯一真源，在内存中同时支持关键词回复和 AI 搜索；MySQL 只保存群申请、定时任务和词条触发日志。`/ai` 使用 Eino ReAct Agent 自主选择关键词并调用内存搜索工具。

## 主要能力

- **关键词回复**：从 WPS 回复表导入 `keyword`、`answer` 和 `aliases`，在群聊中精确匹配。
- **菜单问答**：兼容 `%编号` 菜单树，导入时生成路径，方便回复和检索。
- **AI 问答**：`/ai <问题>` 由 ReAct Agent 使用 AND、OR 或正则表达式搜索当前内存知识库。
- **群管理**：当前群群主和群管理员可执行禁言、NapCat 重启和定时任务命令。
- **引用图**：回复消息后发送 `/q [数量]`，生成最多 10 条消息的动态 GIF 引用图，失败时回退 PNG。
- **分享链接净化**：自动展开 Bilibili、小红书短链，清除分享跟踪参数；支持纯文本和 QQ 小程序卡片。
- **群申请登记**：登记 NapCat 群申请信息，支持管理员同步并在本地按来源群分别导出 Excel。
- **词条统计**：用 MySQL 日志记录关键词回复和 `/ai` 实际搜索命中的知识条目。

## 快速开始

### 1. 准备依赖

本地使用 compose 部署只需要 Docker Compose；如果需要本地运行/调试 bot（make run / go run），还需要 Go 1.25+。`docker-compose.yaml` 会一次启动 MySQL、NapCat、引用图服务和 bot。

### 2. 复制配置

```bash
cp config.example.yaml config.yaml
```

先重点检查这些配置：

- `onebot.access_token`：必须和 NapCat WebSocket token 一致。
- `wps.share_url`：WPS 导出文档链接；为空或下载失败时，启动会尝试读取最后一份有效的本地 XLSX 缓存。
- `wps.sid`：受保护 WPS 文档需要填写，也可用 `JXH_WPS_SID` 注入。
- `database.password`：默认匹配 compose 的 `jxh_password`。
- `ai.api_key`、`ai.model`：配置完整且 `ai.enabled` 开启时才启用 `/ai`。

### 3. 启动全部服务

```bash
make compose-up
```

等价命令：

```bash
NAPCAT_UID=$(id -u) NAPCAT_GID=$(id -g) docker compose up -d --build
```

compose 会同时启动 MySQL、NapCat、quote 和 bot。

持久化数据默认放在仓库根目录的 `./data/` 下，便于直接打包备份和迁移。

### 4. 配置 NapCat

打开 WebUI：

```text
http://127.0.0.1:6099/webui
```

WebUI token 可通过日志查看：

```bash
docker logs napcat
```

登录 QQ 后，开启 OneBot 11 正向 WebSocket：

- 监听地址：`0.0.0.0`
- 监听端口：`3001`
- token：和 `config.yaml` 的 `onebot.access_token` 一致

NapCat 运行在容器内，监听地址不要填 `127.0.0.1`，否则宿主机上的 bot 会连不上。

### 5. 启动 bot

如果你用仓库里的 compose，这一步已经包含在 `make compose-up` 里了，不需要单独再起 bot。

```bash
make run
```

等价命令：

```bash
go run ./cmd/bot -config config.yaml
```

启动后在 QQ 群里发送 `@bot /test`。如果返回 `精小弘正常`，说明接入成功。配置好 WPS 后，发送 `@bot /reload` 导入知识库。

## WPS 知识表

`wps.share_url` 应填写网页端“右键文件 -> 导出文档链接”得到的链接，或可直接下载的 `.xlsx` 地址。

普通 `365.kdocs.cn/l/...` 分享页通常返回 HTML 页面，不能直接导入。受保护文档需要配置 `wps.sid` 或环境变量 `JXH_WPS_SID`。

基础列：

| 列 | 字段 | 说明 |
| --- | --- | --- |
| A | `keyword` | 关键词 |
| B | `answer` | 标准回答 |
| C | 维护备注 | 不入库，不参与回复或 AI 检索 |

可选列：

| 列 | 字段 | 说明 |
| --- | --- | --- |
| D | `aliases` | 同义问法，多个用分隔符隔开 |
| E | `category` | 分类 |
| F | `usage` | 用途控制 |
| G | `status` | 启用状态 |
| H | `source_id` | 稳定 ID，修改 keyword 时用于保留同一条记录 |

`answer` 可以包含图片 CQ 标签，Bot 会在关键词精确回复时把它转换成文字和图片消息。本地图片使用固定的相对路径格式：

```text
校区地图：
[CQ:image,file=maps/campus.png]
```

对应文件放在宿主机的 `data/media/maps/campus.png`。Compose 会把 `data/media/` 只读挂载到 NapCat 的 `/app/jxh-media/`，Bot 发送时会把相对路径转换为该目录下的 `file://` URI。WPS 中只允许使用 `/` 分隔的相对路径；绝对路径、反斜杠、`.`、`..`、查询参数以及直接填写的 `file://`、`base64://` 都会被拒绝。

远程图片同时支持 `http://` 和 `https://`，可以写在 `url` 或 `file` 中；有效的 `url` 优先于 `file`。图片链接无效、本地文件不存在或 NapCat 无法读取时会保留周围文字；如果词条只有图片，Bot 会提示管理员检查图片链接。`/ai` 检索只使用去掉图片标签后的文字，不会向模型发送图片或图片 URL。

导入器会解析 `%编号` 菜单树，并生成 `path` 和 AI 检索用的 `content`。

## 常用命令

群聊里的 `/` 命令需要先 @bot 才会触发，例如 `@bot /test`。普通关键词回复不需要 @bot。

发送带跟踪参数的 `bilibili.com` 链接，或分享 `b23.tv`、`xhslink.com` 以及对应 QQ 小程序卡片时，bot 会额外回复净化后的直链。Bilibili 链接删除全部查询参数；小红书链接仅保留访问所需的 `xsec_token`。

| 命令 | 说明 |
| --- | --- |
| `@bot` | 查看普通命令菜单；关键词和别名无需 @bot |
| `@bot /admin` | 查看管理员命令说明和权限提示 |
| `@bot /test` | 连通性测试 |
| `@bot /reload` | 从 WPS 同步知识库，并刷新缓存 |
| `@bot /ai <问题>` | 让 Agent 自主搜索当前知识库并回答；同时最多处理 2 个请求 |
| `@bot /q [数量]` | 从被回复消息开始生成 1–10 条消息的引用图；默认 1 条 |
| `@bot /admin restart` | 请求 NapCat 重启 |
| `@bot /admin ban <时长> @用户` | 禁言被 @ 的用户；时长支持 `10m`、`1h` 或秒数 |

管理员中文子命令：

| 命令 | 说明 |
| --- | --- |
| `@bot /admin 定时任务 查看` | 查看定时任务 |
| `@bot /admin 定时任务 添加 <每天|单次> <HH:MM> <群聊ID> <消息内容>` | 添加定时任务 |
| `@bot /admin 定时任务 移除 <任务ID>` | 移除定时任务 |
| `@bot /admin 群申请 同步 [数量]` | 从 NapCat 群系统消息补同步近期加群申请，默认 20 条 |
| `@bot /admin 群申请 导出 [全部|最近N]` | 将所有群申请按来源群分别导出到本地 `data/exports/group_requests/` |
| `@bot /admin 词条统计 [7d|30d|全部]` | 将所有群的关键词回复和 `/ai` 检索统计导出到本地 Excel |

bot 会在每次执行 `/admin` 或 `/reload` 时通过 NapCat 查询执行者的实时群角色，只允许当前群群主和群管理员。角色不缓存也不写入 MySQL。NapCat 不能禁言群主、群管理员或机器人自己；禁言失败时 bot 会在群内返回错误原因和该限制提示。

群申请和词条统计面向后台维护人员，导出文件只保存在 bot 本地，不上传到 QQ 群文件。群申请一次查询所有群的数据，并在单次批次目录中按来源群号生成独立 Excel；词条统计跨群汇总为一个 Excel。系统消息中尚未处理的申请状态为 `pending`，已处理但无法判断批准或拒绝的状态为 `observed`。

## 配置和环境变量

主配置文件是 `config.yaml`。示例配置在 `config.example.yaml`，字段说明写在注释里。

常用环境变量：

| 环境变量 | 作用 |
| --- | --- |
| `JXH_ONEBOT_TOKEN` | OneBot WebSocket token |
| `JXH_ONEBOT_WS_URL` | NapCat 正向 WebSocket 地址 |
| `JXH_WPS_SID` | WPS 登录态 sid |
| `JXH_WPS_TIMEOUT_SEC` | WPS 请求超时时间 |
| `MYSQL_DATABASE` | MySQL 数据库名，compose 部署使用 |
| `MYSQL_USER` | MySQL 用户名，compose 部署使用 |
| `MYSQL_PASSWORD` | MySQL 密码，compose 部署使用 |
| `MYSQL_ROOT_PASSWORD` | MySQL root 密码，compose 部署使用 |
| `JXH_MYSQL_PASSWORD` | bot 直连运行时的 MySQL 密码；compose 部署通常用 `MYSQL_PASSWORD` |
| `JXH_MYSQL_DSN` | 完整 MySQL DSN，设置后优先使用 |
| `JXH_AI_PROVIDER` | ChatModel 提供方，支持 `openai`、`ark` |
| `JXH_AI_BASE_URL` | ChatModel base URL |
| `JXH_AI_API_KEY` | ChatModel API Key |
| `JXH_AI_MODEL` | ChatModel 模型名；openai 填模型名，ark 填方舟推理接入点 ID |
| `QQ_QUOTE_REF` | 构建引用图服务使用的 `zjutjh/qq-quote-generator` 分支或 tag，默认 `main` |

AI 行为：

- `ai.enabled: false`：`/ai` 返回未启用。
- 未配置 `ai.api_key` 或 `ai.model`：`/ai` 返回未启用。
- `ai.provider: ark` 时，`ai.model` 填方舟推理接入点 ID，例如 `ep-xxxxxxxx`。
- Agent 必须先搜索知识库，优先使用 AND 精确查询核心词，结果不足时再逐步删减条件、替换同义词或使用 OR/正则放宽。回答只能依据搜索结果；无命中或依据不足时由模型如实说明，不使用自身知识猜测。`7d` 和 `30d` 分别表示应用时区内含今天的最近 7 个和 30 个自然日。

## 引用图服务

引用图由 `zjutjh/qq-quote-generator` 提供。Compose 直接使用该仓库的 Dockerfile 构建，客户端按当前接口将消息统一转换为片段数组，并将 QQ 表情 ID 编码为十进制字符串。服务支持多消息、图文混排、QQ 表情和 GIF/APNG 动画；默认生成 GIF，失败时回退 PNG，无法渲染的空消息会自动忽略。该实现使用 SVG 和 resvg 渲染，运行时不依赖 Chromium。

配置引用图服务:

```yaml
quote:
  base_url: "http://quote:5000"
```

## 数据库和代码生成

项目采用 schema-first，运行时不使用 `AutoMigrate`。表结构以 `deploy/mysql/init/001_schema.sql` 为准。

MySQL 首次初始化时会自动执行该 SQL。最终只包含 `knowledge_trigger_logs`、`scheduled_jobs` 和 `group_join_requests` 三张表，统一使用 `utf8mb4_0900_ai_ci`。本次结构不迁移旧数据，升级时直接重建 `data/mysql`，但不要删除 `data/cache/knowledge.xlsx`。

需要重建空库时：

```bash
docker compose down
rm -rf ./data/mysql
docker compose up -d mysql
```

重新生成 GORM query/model：

```bash
make gormgen-install
export JXH_GORMGEN_DSN="jxh:jxh_password@tcp(127.0.0.1:3306)/jxh_bot?charset=utf8mb4&parseTime=True&loc=Local"
make gormgen
```

更多说明见 `docs/storage-gormgen.md`。

## 开发命令

```bash
make help          # 查看所有 make target
make run           # 本地运行 bot
make build         # 构建 bin/jxh-go
make test          # 运行测试
make fmt           # go fmt ./...
make compose-up    # 启动 mysql 和 napcat
make compose-logs  # 查看 compose 日志
```

## 目录结构

| 路径 | 说明 |
| --- | --- |
| `cmd/bot` | bot 启动入口 |
| `internal/config` | 配置加载、默认值和环境变量覆盖 |
| `internal/bot` | 群消息处理管线和命令路由 |
| `internal/commands` | 群管理和定时任务命令 |
| `internal/knowledge` | WPS 解析、原子内存索引和 Agent 搜索 |
| `internal/ai` | `/ai` ReAct Agent、知识搜索工具和 Eino 模型适配 |
| `internal/storage` | GORM repository、业务存储模型和 generated query/model |
| `internal/triggerstats` | MySQL-backed 词条触发统计 |
| `internal/napcat` | NapCat SDK 适配层 |
| `internal/quote` | 引用图请求和消息内容转换 |
| `internal/scheduler` | 定时任务运行时 |
| `deploy/mysql/init` | MySQL 初始化 SQL |
| `docs` | 设计文档、实现计划和 GORM Gen 说明 |
| `scripts` | 代码生成和工具安装脚本 |
| `data/` | MySQL、NapCat、bot 和 WPS 缓存的持久化根目录 |
| `Dockerfile` | bot 容器镜像构建文件 |
| `docker-compose.yaml` | MySQL、NapCat、quote 和 bot 的完整 compose |
