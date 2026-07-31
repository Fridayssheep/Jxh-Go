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

它通过 NapCat 接入 OneBot 11，以 WPS 回复表作为知识唯一真源，在内存中同时支持关键词回复和 AI 搜索；MySQL 保存管理账号、会话、审计、群申请、功能设置、定时任务、自定义命令和运营统计。`/ai` 使用 Eino ReAct Agent 自主选择关键词并调用内存搜索工具。

## 主要能力

- **关键词回复**：从 WPS 回复表导入 `keyword`、`answer` 和 `aliases`，在群聊中精确匹配。
- **菜单问答**：兼容 `%编号` 菜单树，导入时生成路径，方便回复和检索。
- **AI 问答**：`/ai <问题>` 由 ReAct Agent 使用 AND、OR 或正则表达式搜索当前内存知识库，并保持温和友好的精小弘人设。
- **群管理**：当前群群主和群管理员可执行禁言、NapCat 重启和定时任务命令。
- **引用图**：回复消息后发送 `/q [数量]`，生成最多 10 条消息的动态 GIF 引用图，失败时回退 PNG。
- **分享链接净化**：自动展开 Bilibili、小红书短链，清除分享跟踪参数；支持纯文本和 QQ 小程序卡片。
- **群申请登记**：实时登记 NapCat 群申请，每 10 秒自动同步处理状态，并用 AI 提取学号、姓名和专业。
- **词条统计**：用 MySQL 日志记录成功发送的关键词回复和 `/ai` 实际搜索命中的知识条目，导出时按词条合并两类次数。

## 快速开始

### 1. 准备依赖

本地使用 compose 部署只需要 Docker Compose；如果需要本地运行/调试 bot（make run / go run），还需要 Go 1.25+。`docker-compose.yaml` 会一次启动 MySQL、NapCat、引用图服务和 bot。

### 2. 准备配置

本地运行时复制示例配置：

```bash
cp config.example.yaml config.yaml
```

使用 Compose 时无需执行这条命令；首次启动会自动创建 `./data/config/config.yaml`，之后由管理面板或管理员直接维护该持久化文件。

先重点检查这些配置：

- `onebot.access_token`：必须和 NapCat WebSocket token 一致。
- `wps.share_url`：WPS 导出文档链接；为空或下载失败时，启动会尝试读取最后一份有效的本地 XLSX 缓存。
- `wps.sid`：受保护 WPS 文档需要填写，也可用 `JXH_WPS_SID` 注入。
- `database.password`：默认匹配 compose 的 `jxh_password`。
- `ai.api_key`、`ai.model`：配置完整且 `ai.enabled` 开启时才启用 `/ai`。
- `admin.public_origin`、`admin.session_secret`：管理面板的唯一浏览器 Origin 和至少 32 字节随机主密钥；生产环境必须使用 HTTPS 并保持 `cookie_secure: true`。
- `admin.max_concurrent_requests`：管理 API 同时处理的请求上限，超限立即返回可重试的 503，避免管理流量占满 Bot 进程资源。

`admin.public_origin` 为空或 `admin.session_secret` 不足 32 字节时，进程会把 Admin 标记为 `misconfigured`，不启动管理 API、管理端自定义命令、遥测和管理 worker；原 QQ Bot、`/healthz`、NapCat、原定时任务和申请处理继续运行。

### 3. 启动全部服务

```bash
make compose-up
```

等价命令：

```bash
NAPCAT_UID=$(id -u) NAPCAT_GID=$(id -g) docker compose up -d --build
```

compose 会同时启动 MySQL、NapCat、quote 和 bot。

Compose 会把运行配置保存在 `./data/config/config.yaml`。首次启动时 entrypoint 会从镜像内模板初始化该文件，后续启动不会覆盖管理面板写入的设置；bot 收到受控重启请求后以退出码 75 结束，由 `restart: unless-stopped` 自动拉起。

管理 API 默认监听 `http://127.0.0.1:8090/api/admin/v1`。首次使用前执行迁移并创建唯一的首个超级管理员：

```bash
make migrate
printf '%s\n' 'replace-with-a-strong-password' | go run ./cmd/admin-bootstrap -config config.yaml -username admin -display-name 管理员 -password-stdin
```

密码不会出现在命令参数中；生产环境优先直接运行命令并通过终端隐藏输入。创建成功后该命令会拒绝再次引导，后续账号统一由管理 API 创建。

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

启动后在 QQ 群里发送 `/test`。如果返回 `精小弘正常`，说明接入成功。配置好 WPS 后，发送 `/reload` 导入知识库。

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

`answer` 可以包含图片和文件 CQ 标签，Bot 会在关键词精确回复时按原顺序发送文字、图片和 QQ 闪传附件。本地媒体使用固定的相对路径格式：

```text
校区地图：
[CQ:image,file=maps/campus.png]
[CQ:file,file=培养计划/人工智能.pdf]
```

对应文件分别放在宿主机的 `data/media/maps/campus.png` 和 `data/media/培养计划/人工智能.pdf`。Compose 会把 `data/media/` 只读挂载到 NapCat 的 `/app/jxh-media/`。WPS 中只允许使用 `/` 分隔的相对路径；绝对路径、反斜杠、`.`、`..`、查询参数以及直接填写的 `file://`、`base64://` 都会被拒绝。

远程图片和文件同时支持 `http://` 与 `https://`，可以写在 `url` 或 `file` 中；有效的 `url` 优先于 `file`。文件显示名自动取 URL 或相对路径的最后一段，不需要额外参数，例如：

```text
[CQ:file,file=https://cube.phlin.cn/files/qqbot/2026培养计划/人工智能.pdf]
```

远程文件由 bot 下载到 `data/flash/` 后交给 NapCat，通过 QQ 闪传发送为聊天里的临时附件，不会上传到群文件。下载限制为单文件 100 MiB、最多 3 次跳转、2 分钟和同时 2 个任务，并拒绝凭据、非常用端口及内网/本机目标。相同来源在 24 小时内复用暂存文件；暂存总量最多 512 MiB、128 个文件，后续下载时会清理超过 24 小时的内容。图片或文件无效时会在对应位置给出提示并继续发送后续内容；失败提示不会回显可能含签名参数的源 URL。`/ai` 检索只使用去掉图片和文件标签后的文字，不会向模型发送媒体内容或 URL。

导入器会解析 `%编号` 菜单树，并生成 `path` 和 AI 检索用的 `content`。

## 常用命令

除 `/admin` 外，群聊里的 `/` 命令都可以直接触发，例如 `/test`。`/admin` 及其子命令必须先 @bot；@bot 但不附带命令时会显示命令菜单，普通关键词回复也不需要 @bot。

发送带跟踪参数的 `bilibili.com` 链接，或分享 `b23.tv`、`xhslink.com` 以及对应 QQ 小程序卡片时，bot 会额外回复净化后的直链。Bilibili 链接删除全部查询参数；小红书链接仅保留访问所需的 `xsec_token`。

| 命令 | 说明 |
| --- | --- |
| `@bot` | 查看普通命令菜单；关键词和别名无需 @bot |
| `@bot /admin` | 查看管理员命令说明和权限提示；所有 `/admin` 命令都必须 @bot |
| `/test` | 连通性测试 |
| `/reload` | 从 WPS 同步知识库，并刷新缓存 |
| `/ai <问题>` | 让 Agent 自主搜索当前知识库并回答；同时最多处理 2 个请求 |
| `/q [数量]` | 生成被回复消息及其之前的 1–10 条消息引用图；默认 1 条 |
| `@bot /admin restart` | 请求 NapCat 重启 |
| `@bot /admin ban <时长> @用户1 [@用户2 ...]` | 批量禁言被 @ 的用户；时长支持 `10m`、`1h` 或秒数 |

管理员中文子命令：

| 命令 | 说明 |
| --- | --- |
| `@bot /admin 定时任务 查看` | 查看定时任务 |
| `@bot /admin 定时任务 添加 每天 <HH:MM> <当前群ID> <消息内容>` | 为当前群添加每日任务 |
| `@bot /admin 定时任务 添加 单次 <YYYY-MM-DD HH:MM> <当前群ID> <消息内容>` | 为当前群添加指定日期执行的单次任务 |
| `@bot /admin 定时任务 移除 <任务ID>` | 移除当前群的定时任务 |
| `@bot /admin 群申请 导出 [数量]` | 不填数量时导出全部；填写正整数时导出最新 N 条，并按来源群分别保存到本地 `data/exports/group_requests/` |
| `@bot /admin 词条统计 [7d|30d|全部]` | 将所有群的关键词回复和 `/ai` 检索统计导出到本地 Excel |

bot 只处理明确 @bot 的 `/admin` 命令，并会在每次执行 `/admin` 或 `/reload` 时通过 NapCat 查询执行者的实时群角色，只允许当前群群主和群管理员。角色不缓存也不写入 MySQL。定时任务按群隔离，只能在当前群查看、添加和移除。NapCat 不能禁言群主、群管理员或机器人自己；禁言失败时 bot 会在群内返回错误原因和该限制提示。

群申请和词条统计面向后台维护人员，导出文件只保存在 bot 本地，不上传到 QQ 群文件。群申请事件会实时入库；每次连接 NapCat 后立即读取最近 100 条群系统消息，之后每 10 秒自动同步一次。系统消息中尚未处理的申请状态为 `pending`，已处理但无法判断批准或拒绝的状态为 `processed`，记录不会因处理完成而删除。启用 AI 时，加入申请会异步提取学号、姓名和专业；存在“答案：”时只把答案部分发送给模型，单个字段不可信时只丢弃该字段。原始验证信息先入库，解析失败不会丢失申请；执行 `006_reparse_group_request_applicants.sql` 后，历史 `add` 申请中尚未完成 AI 解析的记录会重新排队。部署时应确认模型服务的数据处理政策。导出一次查询所有群的数据，并在单次批次目录中按来源群号生成独立 Excel；词条统计跨群汇总为一个 Excel。

## 配置和环境变量

本地运行时主配置文件是 `config.yaml`；Compose 部署时是持久化目录中的 `./data/config/config.yaml`。示例配置在 `config.example.yaml`，字段说明写在注释里。

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
| `PUID` / `PGID` | Compose 中 bot 进程使用的 UID/GID，NAS 挂载目录可据此匹配宿主权限 |
| `JXH_BOT_RESTART_MODE` | Bot 重启模式；仅在 Docker/systemd 等进程监督器下使用 `supervised_exit`，Compose 已自动设置 |
| `JXH_AI_PROVIDER` | ChatModel 提供方，支持 `openai`、`ark` |
| `JXH_AI_BASE_URL` | ChatModel base URL |
| `JXH_AI_API_KEY` | ChatModel API Key |
| `JXH_AI_MODEL` | ChatModel 模型名；openai 填模型名，ark 填方舟推理接入点 ID |
| `QQ_QUOTE_REF` | 构建引用图服务使用的 `zjutjh/qq-quote-generator` 分支或 tag，默认 `main` |

AI 行为：

- `ai.enabled: false`：`/ai` 返回未启用，新群申请跳过 AI 字段提取。
- 未配置 `ai.api_key` 或 `ai.model`：`/ai` 返回未启用，新群申请跳过 AI 字段提取。
- `ai.provider: ark` 时，`ai.model` 填方舟推理接入点 ID，例如 `ep-xxxxxxxx`。
- Agent 必须先搜索知识库，优先使用 AND 精确查询核心词，结果不足时再逐步删减条件、替换同义词或使用 OR/正则放宽。单次回答最多实际搜索三次，第三次返回后必须使用已有结果收束回答；无命中时由程序返回温和的提问引导，不放行模型自由回答。每个有知识依据的候选回答都会额外调用一次同配置的独立审查模型，以结构化 JSON 判断是否允许、重写或拒绝；简体、繁体、变体字和夹杂外语适用相同规则。精小弘必须保持温和友好的迎新助手身份，冷酷、暴躁等风格请求只降级为无攻击性的直白表达或轻度玩梗，不模仿现实人物或其他人格。审查调用失败、响应格式错误或拒绝回答时返回固定友好提示，不放行未经审查的候选回答。`7d` 和 `30d` 分别表示应用时区内含今天的最近 7 个和 30 个自然日。

## 引用图服务

引用图由 `zjutjh/qq-quote-generator` 提供。Compose 直接使用该仓库的 Dockerfile 构建，客户端按当前接口将消息统一转换为片段数组，并将 QQ 表情 ID 编码为十进制字符串；`@` 成员优先显示当前群名片，其次显示 QQ 昵称。服务支持多消息、图文混排、QQ 表情和 GIF/APNG 动画；默认生成 GIF，失败时回退 PNG，无法渲染的空消息会自动忽略。该实现使用 SVG 和 resvg 渲染，运行时不依赖 Chromium。

配置引用图服务:

```yaml
quote:
  base_url: "http://quote:5000"
```

## 数据库

项目采用 schema-first，运行时不使用 `AutoMigrate`。表结构以 `deploy/mysql/init/001_schema.sql` 为准。

MySQL 首次初始化时会自动执行该 SQL。初始化脚本只会在空数据目录首次启动时执行；已有部署由 `jxh-migrate` 按 `deploy/mysql/migrations/` 的版本顺序升级。迁移器使用数据库锁避免并发执行，并校验已应用脚本的名称和校验和。Compose 中的 `bot` 依赖一次性 `migrate` 服务成功完成，迁移失败时不会在旧 schema 上启动。

本地应用迁移并查看已经记录的版本：

```bash
make migrate
make migration-status
```

发布前先备份数据库。需要回滚时停止 bot，恢复升级前的完整备份，再部署与该 schema 匹配的旧版本；不要通过修改已经发布的 SQL 或手工删除 `schema_migrations` 记录来回滚。迁移文件发布后不可变，修正已有迁移必须追加更高版本脚本。

需要重建空库时：

```bash
docker compose down
rm -rf ./data/mysql
docker compose up -d mysql
```

## 开发命令

```bash
make help          # 查看所有 make target
make run           # 本地运行 bot
make build         # 构建 bin/jxh-go
make test          # 编译检查所有 Go 包
make fmt           # go fmt ./...
make migrate       # 应用待执行的数据库迁移
make migration-status # 查看已应用的数据库迁移
make compose-up    # 启动 mysql 和 napcat
make compose-logs  # 查看 compose 日志
```

## 目录结构

| 路径 | 说明 |
| --- | --- |
| `cmd/` | Bot、数据库迁移和首个管理员引导入口 |
| `internal/management/` | 管理 API、鉴权审计、设置、总览、统计和系统操作 |
| `internal/automation/` | 自定义命令、定时任务管理和调度运行时 |
| `internal/groups/` | 群目录、入群申请采集和审批状态机 |
| `internal/knowledge/` | WPS 知识索引、管理视图和词条触发统计 |
| `internal/messaging/` | CQ 回复、引用图、链接净化和远程文件暂存 |
| `internal/bot/` | 群消息处理管线、命令路由和旧群聊管理命令 |
| `internal/ai/` | `/ai` ReAct Agent、知识搜索工具和申请字段提取 |
| `internal/platform/` | 应用生命周期、配置、数据库、GORM 存储、NapCat、健康和遥测 |
| `deploy/mysql/init` | MySQL 初始化 SQL |
| `deploy/mysql/migrations` | 已有部署按顺序手工执行的 schema 迁移 SQL |
| `data/` | MySQL、NapCat、bot 和 WPS 缓存的持久化根目录 |
| `Dockerfile` | bot 容器镜像构建文件 |
| `docker-compose.yaml` | MySQL、NapCat、quote 和 bot 的完整 compose |

`internal/README.md` 记录各功能目录的职责和新增包放置规则。
