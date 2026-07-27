.DEFAULT_GOAL := help

GO ?= go
CONFIG ?= config.yaml
BINARY ?= bin/jxh-go
COMPOSE ?= docker compose
NAPCAT_UID ?= $(shell id -u)
NAPCAT_GID ?= $(shell id -g)
.PHONY: help run build test fmt tidy clean migrate migration-status compose-up compose-down compose-logs mysql napcat

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

run: ## Run the bot locally
	$(GO) run ./cmd/bot -config $(CONFIG)

build: ## Build the bot binary
	@mkdir -p $(dir $(BINARY))
	$(GO) build -o $(BINARY) ./cmd/bot

test: ## Compile-check all Go packages
	$(GO) test ./...

fmt: ## Format Go source files
	$(GO) fmt ./...

tidy: ## Tidy Go module dependencies
	$(GO) mod tidy

migrate: ## Apply pending database migrations
	$(GO) run ./cmd/migrate -config $(CONFIG) -dir deploy/mysql/migrations

migration-status: ## Show applied database migrations
	$(COMPOSE) exec mysql sh -c 'MYSQL_PWD="$$MYSQL_PASSWORD" mysql -u"$$MYSQL_USER" "$$MYSQL_DATABASE" -e "SELECT version, name, applied_at FROM schema_migrations ORDER BY version"'

clean: ## Remove build artifacts
	rm -rf $(dir $(BINARY))

compose-up: ## Start the full compose stack
	NAPCAT_UID=$(NAPCAT_UID) NAPCAT_GID=$(NAPCAT_GID) $(COMPOSE) up -d --build

compose-down: ## Stop local external dependencies
	$(COMPOSE) down

compose-logs: ## Follow Docker Compose logs
	$(COMPOSE) logs -f

mysql: ## Start MySQL only
	$(COMPOSE) up -d mysql

napcat: ## Start NapCat only
	NAPCAT_UID=$(NAPCAT_UID) NAPCAT_GID=$(NAPCAT_GID) $(COMPOSE) up -d napcat
