# Local CQ Image Mount Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Send keyword-reply images from a fixed local media directory and from HTTP or HTTPS URLs.

**Architecture:** Extend the narrow CQ image parser to resolve safe relative `file` values to the fixed NapCat media mount. Bind-mount `data/media/` read-only into NapCat; preserve the existing structured OneBot sender and text fallback.

**Tech Stack:** Go 1.25, OneBot 11/NapCat, Docker Compose.

---

### Task 1: Lock the source resolution contract

**Files:**
- Modify: `internal/cqreply/parser_test.go`

- [x] Add failing table tests for HTTP URLs, `url` precedence, safe local paths, Unicode/space URI encoding, traversal, absolute paths, backslashes, and unsupported schemes.
- [x] Run `go test ./internal/cqreply -count=1` and confirm the new local/HTTP cases fail for the missing behavior.

### Task 2: Implement local and remote source resolution

**Files:**
- Modify: `internal/cqreply/parser.go`

- [x] Replace HTTPS-only validation with HTTP/HTTPS URL validation.
- [x] Resolve safe relative `file` values beneath `/app/jxh-media` as encoded `file://` URIs.
- [x] Run `go test ./internal/cqreply -count=1` and confirm all parser tests pass.

### Task 3: Mount and document local media

**Files:**
- Modify: `docker-compose.yaml`
- Modify: `.gitignore`
- Modify: `README.md`

- [x] Add `./data/media:/app/jxh-media:ro` to NapCat.
- [x] Ignore `data/media/` and document the fixed WPS/host/container path mapping plus HTTP support.
- [x] Run `docker compose config --quiet` and confirm exit code 0.

### Task 4: Verify the complete feature

**Files:**
- Test: `internal/cqreply/parser_test.go`
- Test: `internal/bot/pipeline_cq_reply_test.go`
- Test: `internal/knowledge/parser_cq_test.go`

- [x] Run `go test -count=1 ./...`.
- [x] Run `go test -race -count=1 ./...`.
- [x] Run `go vet ./...` and `git diff --check`.
- [x] Run `docker compose build bot` and confirm the image builds successfully.
