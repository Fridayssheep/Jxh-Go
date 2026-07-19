# Group Request Excel Delivery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upload each successfully exported group-request Excel workbook to the current QQ group's group files and report a useful local fallback when upload fails.

**Architecture:** Add a capability interface in the bot command router so export behavior remains testable without NapCat. Implement that capability on `napcat.SDKSender` with the SDK's typed `UploadGroupFile` API, while keeping Excel generation and persistence unchanged.

**Tech Stack:** Go 1.25, NapCat SDK v1.0.0, OneBot/NapCat `upload_group_file`, Go `testing`, `httptest`.

---

### Task 1: Upload exported workbooks from the command router

**Files:**
- Modify: `internal/bot/command_router.go`
- Modify: `internal/bot/command_router_test.go`

- [x] **Step 1: Write the failing success-path test**

Add a sender that records `UploadGroupFile` arguments:

```go
type groupRequestExportSender struct {
	recordingModerator
	uploadGroupID int64
	uploadPath    string
	uploadName    string
	uploadErr     error
}

func (s *groupRequestExportSender) UploadGroupFile(ctx context.Context, groupID int64, path, name string) error {
	_ = ctx
	s.uploadGroupID = groupID
	s.uploadPath = path
	s.uploadName = name
	return s.uploadErr
}
```

Update the owner export test to assert group `123`, an existing exported path, basename `group_requests_20260710_203000.xlsx`, and confirmation text `已导出群申请 1 条，Excel 已发送到群文件`.

- [x] **Step 2: Run the focused test and verify RED**

Run: `go test -count=1 ./internal/bot -run TestGroupCommandRouterExportsGroupRequestsForOwner`

Expected: FAIL because the router does not call `UploadGroupFile` and still returns the server path message.

- [x] **Step 3: Add the capability interface and minimal success path**

Add:

```go
type GroupFileUploader interface {
	UploadGroupFile(ctx context.Context, groupID int64, path, name string) error
}
```

After `groupRequests.Export`, assert `sender.(GroupFileUploader)`, call it with `msg.GroupID`, `result.Path`, and `filepath.Base(result.Path)`, then send the approved success text.

- [x] **Step 4: Run the focused test and verify GREEN**

Run: `go test -count=1 ./internal/bot -run TestGroupCommandRouterExportsGroupRequestsForOwner`

Expected: PASS.

- [x] **Step 5: Write failing fallback tests**

Add one test with `uploadErr: errors.New("upload unavailable")` and assert the response contains the error and exported path while the file still exists. Update the sender-without-capability test to assert `群文件上传接口未初始化` and the exported path.

- [x] **Step 6: Run fallback tests and verify RED**

Run: `go test -count=1 ./internal/bot -run 'TestGroupCommandRouterExport.*(Fails|Unavailable)'`

Expected: FAIL until both fallback messages are implemented.

- [x] **Step 7: Implement fallback messages**

Return `sender.SendGroupText` with the exported count, upload error or missing-interface reason, and `result.Path`. Do not remove the generated file and do not propagate the upload error after feedback succeeds.

- [x] **Step 8: Run bot package tests**

Run: `go test -count=1 ./internal/bot`

Expected: PASS.

### Task 2: Implement NapCat group-file upload

**Files:**
- Modify: `internal/napcat/adapter.go`
- Modify: `internal/napcat/adapter_test.go`

- [x] **Step 1: Write the failing adapter request test**

Use `httptest.Server` and `napcatsdk.NewHTTPClient` to capture `POST /upload_group_file`. Decode the body into `api.UploadGroupFileRequest` and assert:

```go
api.UploadGroupFileRequest{
	GroupID:   "123",
	File:      "data/exports/group_requests/test.xlsx",
	Name:      "test.xlsx",
	UploadFile: true,
}
```

Return `{"status":"ok","retcode":0,"data":{"file_id":"file-1"}}` from the test server.

- [x] **Step 2: Run the focused test and verify RED**

Run: `go test -count=1 ./internal/napcat -run TestSDKSenderUploadsGroupFile`

Expected: build failure because `SDKSender.UploadGroupFile` does not exist.

- [x] **Step 3: Implement the typed SDK call**

Add to `SDKSender`:

```go
func (s SDKSender) UploadGroupFile(ctx context.Context, groupID int64, path, name string) error {
	_, err := s.client.API().UploadGroupFile(ctx, api.UploadGroupFileRequest{
		GroupID:   strconv.FormatInt(groupID, 10),
		File:      path,
		Name:      name,
		UploadFile: true,
	})
	return err
}
```

- [x] **Step 4: Run NapCat package tests**

Run: `go test -count=1 ./internal/napcat`

Expected: PASS.

### Task 3: Documentation and verification

**Files:**
- Modify: `README.md`
- Modify: `docker-compose.yaml`

- [x] **Step 1: Update command documentation**

Change the group-request export description to state that the generated Excel is uploaded to the current group files, with a local server-path fallback when upload fails.

- [x] **Step 2: Share the export directory with NapCat**

Add `./data/exports:/app/napcat/data/exports:ro` to the NapCat service. Its entrypoint changes the runtime working directory to `/app/napcat`, so the relative `data/exports/...` path sent by the bot resolves to the shared file.

- [x] **Step 3: Format modified Go files**

Run: `gofmt -w internal/bot/command_router.go internal/bot/command_router_test.go internal/napcat/adapter.go internal/napcat/adapter_test.go`

Expected: files are formatted without output.

- [x] **Step 4: Run focused race tests**

Run: `go test -race -count=1 ./internal/bot ./internal/napcat`

Expected: PASS.

- [x] **Step 5: Run the complete test suite**

Run: `go test -count=1 ./...`

Expected: PASS.

- [x] **Step 6: Check Compose, the diff, and build the bot image**

Run: `docker compose config`

Expected: the NapCat service includes a read-only bind mount from `data/exports` to `/app/napcat/data/exports`.

Run: `git diff --check`

Expected: no output and exit code 0.

Run: `docker compose build bot`

Expected: image `jxh-go-bot` builds successfully.

- [x] **Step 7: Confirm scope**

Inspect `git diff` and confirm only the approved group-file behavior, tests, README text, and this implementation plan were added on top of the existing uncommitted feature work. Do not commit the overlapping production files automatically.
