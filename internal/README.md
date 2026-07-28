# Internal Package Layout

`internal` 按业务能力组织，而不是按技术类型平铺。包名保持短小，目录层级负责表达所属功能。

| 目录 | 职责 |
| --- | --- |
| `management/` | 管理后端组合、HTTP API、认证授权、审计、事件、设置、总览、统计和系统操作 |
| `automation/` | 自定义命令、定时任务资源和定时执行运行时 |
| `groups/` | 群目录，以及 `grouprequest` 采集和 `joinrequests` 审批 |
| `knowledge/` | WPS 导入、内存索引、管理查询和触发统计 |
| `messaging/` | 消息内容解析与转换、引用图、链接净化和文件暂存 |
| `bot/` | QQ 消息管线、内置命令路由和群聊管理命令 |
| `ai/` | 模型客户端编排、知识问答和申请字段提取 |
| `platform/` | 进程生命周期、配置、数据库、存储、NapCat、健康检查、遥测和通用安全运行工具 |

放置新代码时遵循以下规则：

1. 业务规则放在对应功能目录，由小型 Store 或 Gateway 接口描述依赖。
2. MySQL/GORM 实现统一放在 `platform/storage`，OneBot/NapCat 适配统一放在 `platform/napcat`。
3. HTTP DTO、Cookie 和中间件只放在 `management/api`，不向业务包泄漏 HTTP 类型。
4. 跨功能装配放在 `management`、`platform/app` 或 `cmd`，不要新增只做转发的公共包。
5. 只有无法归属现有功能边界时才增加新的一级目录，并同步更新本文件。
