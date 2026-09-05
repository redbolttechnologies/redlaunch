.PHONY: build test lint run setup fmt compose-config docker-build clean add-authorized-email auth-add-email

BINARY := bin/redlaunch
GO_PACKAGES := ./cmd/... ./internal/...

build: ## Build the server binary
	go build -trimpath -o $(BINARY) ./cmd/redlaunch

test: ## Run tests with the race detector
	go test -race -count=1 $(GO_PACKAGES)

lint: ## Run the standard Go static checks
	go vet $(GO_PACKAGES)

fmt: ## Format Go source files
	gofmt -w $$(rg --files -g '*.go')

run: build ## Build and run with local defaults
	SYSTEMD_SCOPE=user \
	SYSTEMD_UNIT_DIR="$${XDG_CONFIG_HOME:-$$HOME/.config}/systemd/user" \
	BACKUP_ROOT=./data/backups \
	./$(BINARY)

setup: ## Configure a fresh VPS and start Redlaunch with Docker Compose
	bash scripts/setup.sh

compose-config: ## Validate the Docker Compose configuration
	docker compose config

docker-build: ## Build the production image
	docker build -t redlaunch:local .

clean: ## Remove local build output
	rm -rf bin

export EMAIL

add-authorized-email: ## Add a Google email address to the login allowlist (EMAIL=...)
	@test -n "$${EMAIL}" || { echo "usage: make add-authorized-email EMAIL=you@example.com" >&2; exit 1; }
	go run ./cmd/redlaunch auth-add-email --email "$${EMAIL}"

auth-add-email: add-authorized-email ## Alias for add-authorized-email
