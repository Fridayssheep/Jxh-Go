# 群申请 Excel 群文件发送设计

## 目标

管理员在群聊执行 `@bot /admin 群申请 导出 [全部|最近N]` 后，bot 生成群申请 Excel，并通过 NapCat 上传到当前 QQ 群的群文件。QQ 客户端可在聊天消息流中展示群文件通知或卡片，群成员可直接打开下载。

## 范围

- 保留现有 Excel 生成目录、文件名和列结构。
- 保留现有管理员权限检查和群聊 `@bot` 门控。
- 上传目标固定为执行命令的当前群。
- 上传文件名使用导出文件的 basename。
- 上传成功后发送文本确认。
- 上传失败时保留本地文件，并发送失败原因和服务器路径。

本次不增加私聊文件发送、上传重试、群文件目录管理、历史导出文件清理或自动审批功能。

## 设计

### 命令路由

在 `internal/bot` 定义小型能力接口：

```go
type GroupFileUploader interface {
	UploadGroupFile(ctx context.Context, groupID int64, path, name string) error
}
```

`/admin 群申请 导出` 的处理顺序：

1. 校验管理员权限和导出参数。
2. 调用群申请服务生成 Excel。
3. 判断当前 sender 是否实现 `GroupFileUploader`。
4. 使用当前群号、导出路径和文件 basename 上传群文件。
5. 根据上传结果发送文本反馈。

上传成功回复：

```text
已导出群申请 N 条，Excel 已发送到群文件
```

sender 不支持上传时回复：

```text
已导出群申请 N 条，但群文件上传接口未初始化。文件保存在：<path>
```

NapCat 上传失败时回复：

```text
已导出群申请 N 条，但上传群文件失败：<error>。文件保存在：<path>
```

上传失败属于可恢复的交付失败：Excel 已经生成，因此命令处理函数发送明确反馈后返回文本发送结果，不把原始上传错误继续向上抛出。

### NapCat adapter

`internal/napcat.SDKSender` 实现 `UploadGroupFile`，调用 SDK 的 `api.Client.UploadGroupFile`：

- `GroupID`：当前群号的十进制字符串。
- `File`：导出文件路径。
- `Name`：导出文件 basename。
- `UploadFile`：`true`。
- `Folder` 和 `FolderID`：留空，上传到群文件根目录。

不使用 `send_online_file`，因为当前 SDK 的该接口只接受 `user_id`，用于私聊文件，不支持群聊目标。

## 错误处理

- Excel 生成失败：保持现有行为，返回错误，不尝试上传。
- sender 不支持群文件上传：保留 Excel，向群内返回接口未初始化和文件路径。
- NapCat 上传失败：保留 Excel，向群内返回错误和文件路径。
- 成功上传后文本确认发送失败：返回文本发送错误，由现有消息管线记录。

## 测试

- 命令路由测试：导出成功后调用上传接口，群号、路径和文件名正确。
- 命令路由测试：上传成功时发送成功确认文本。
- 命令路由测试：上传失败时发送错误和本地路径，Excel 文件仍存在。
- 命令路由测试：sender 不支持上传时发送接口未初始化和本地路径。
- NapCat adapter 测试：确认调用 action 为 `upload_group_file`，请求参数符合设计。
- 回归测试：未授权用户不能生成或上传文件；群聊命令仍要求 `@bot`。

## 验收标准

- 自动化测试通过。
- Docker bot 镜像构建成功。
- 使用真实 NapCat 时，命令生成 Excel 并在当前群产生可下载的群文件通知或卡片。
- 上传失败不会删除导出的 Excel，群内反馈包含可排查的错误信息和本地路径。
