.PHONY: build test integration lint fmt fmt-check secret-scan run setup update linux-build vulncheck-linux compose-config docker-build image-identity release-check clean add-authorized-email auth-add-email

BINARY := bin/redlaunch
GO_VERSION := 1.26.8
GO_TOOLCHAIN := go$(GO_VERSION)
GO := GOTOOLCHAIN=$(GO_TOOLCHAIN) go
GO_PACKAGES := ./cmd/... ./internal/...
GOVULNCHECK_VERSION := v1.8.0
LINUX_ARCH ?= amd64
LINUX_BINARY := bin/redlaunch-linux-$(LINUX_ARCH)
IMAGE := redlaunch:local
IMAGE_IDENTITY := bin/redlaunch-image.identity

build: ## Build the server binary
	$(GO) build -trimpath -o $(BINARY) ./cmd/redlaunch

test: ## Run tests with the race detector
	$(GO) test -race -count=1 $(GO_PACKAGES)

integration: ## Run opt-in Docker/systemd/recovery checks on disposable fixtures (skips without docker)
	REDLAUNCH_DOCKER_CONFIG_TEST=1 $(GO) test -race -count=1 $(GO_PACKAGES)

lint: ## Run the standard Go static checks
	$(GO) vet $(GO_PACKAGES)

fmt: ## Format Go source files
	$(GO) fmt $(GO_PACKAGES)

fmt-check: ## Fail when Go source files need formatting
	test -z "$$(gofmt -l cmd internal)" || { echo "unformatted Go files:" >&2; gofmt -l cmd internal >&2; exit 1; }

secret-scan: ## Fail when real environment files, keys, or private-key material are tracked by git
	git ls-files | grep -E '(^|/)\.env$$|(^|/)vars\.env$$|(^|/)secrets\.env$$|\.pem$$|\.key$$|(^|/)credentials[^/]*\.json$$' | grep -v '\.example' > /dev/null && { echo "tracked secret files found (see .gitignore)" >&2; exit 1; } || true
	! git grep -l --cached 'BEGIN .*PRIVATE 'KEY -- . ':!*.example*' > /dev/null || { echo "tracked private-key material found" >&2; exit 1; }

linux-build: ## Build the production Linux binary and record module/toolchain identity
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) $(GO) build -trimpath -ldflags="-s -w" -o $(LINUX_BINARY) ./cmd/redlaunch
	$(GO) version -m $(LINUX_BINARY) > $(LINUX_BINARY).buildinfo

vulncheck-linux: linux-build ## Scan the compiled Linux release artifact
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) -mode=binary $(LINUX_BINARY)

run: build ## Build and run with local defaults
	SYSTEMD_SCOPE=user \
	SYSTEMD_UNIT_DIR="$${XDG_CONFIG_HOME:-$$HOME/.config}/systemd/user" \
	BACKUP_ROOT=./data/backups \
	./$(BINARY)

setup: ## Configure a fresh VPS and start Redlaunch with Docker Compose
	bash scripts/setup.sh

update: ## Pull the latest changes and rebuild the Docker Compose deployment
	git pull
	docker compose up -d --build

compose-config: ## Validate the Docker Compose configuration
	docker compose --env-file /dev/null config --quiet

docker-build: ## Build the production image
	docker build --build-arg GO_VERSION=$(GO_VERSION) -t $(IMAGE) .

image-identity: docker-build ## Record the local release image ID, digests, and compiler label
	mkdir -p bin
	docker image inspect --format 'image_id={{.Id}} repo_digests={{json .RepoDigests}} go_version={{index .Config.Labels "io.redlaunch.build.go-version"}}' $(IMAGE) > $(IMAGE_IDENTITY)

release-check: test integration lint fmt-check secret-scan vulncheck-linux compose-config image-identity ## Run the release gate

clean: ## Remove local build output
	rm -rf bin

export EMAIL

add-authorized-email: ## Add a Google email address to the login allowlist (EMAIL=...)
	@test -n "$${EMAIL}" || { echo "usage: make add-authorized-email EMAIL=you@example.com" >&2; exit 1; }
	$(GO) run ./cmd/redlaunch auth-add-email --email "$${EMAIL}"

auth-add-email: add-authorized-email ## Alias for add-authorized-email
