set dotenv-load := true

GO := env_var_or_default("GO", "go")
BIN := "bin/sentra"
PKG := "./..."
SENTRA := "./bin/sentra"
MINIO_ACCESS_KEY := env_var_or_default("MINIO_ROOT_USER", "minioadmin")
MINIO_SECRET_KEY := env_var_or_default("MINIO_ROOT_PASSWORD", "minioadmin")
DEMO_DIR := env_var_or_default("SENTRA_DEMO_DIR", "demo-data")
RESTORE_DIR := env_var_or_default("SENTRA_RESTORE_DIR", "/tmp/sentra-restored")

build:
	{{GO}} build -o {{BIN}} ./cmd/sentra

test:
	{{GO}} test -race -coverprofile=coverage.out {{PKG}}

integration:
	{{GO}} test -race -tags=integration ./...

lint:
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "golangci-lint is required for 'just lint'."; \
		echo "Install it with: brew install golangci-lint"; \
		echo "Or: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
		exit 127; \
	fi
	golangci-lint run

fmt:
	{{GO}} fmt {{PKG}}

vet:
	{{GO}} vet {{PKG}}

tidy:
	{{GO}} mod tidy

clean:
	rm -rf bin coverage.out

# Print the environment exports used by local MinIO recipes.
local-env:
	@echo "export AWS_ACCESS_KEY_ID={{MINIO_ACCESS_KEY}}"
	@echo "export AWS_SECRET_ACCESS_KEY={{MINIO_SECRET_KEY}}"
	@echo "export SENTRA_PASSPHRASE='<choose-a-local-passphrase>'"

# Start local MinIO and create the sentra-test bucket.
local-up:
	docker compose up -d

# Show local MinIO container status.
local-status:
	docker compose ps

# Follow local MinIO logs.
local-logs:
	docker compose logs -f minio createbuckets

# Stop local MinIO without deleting stored data.
local-down:
	docker compose down

# Stop local MinIO and delete demo data, restore output, and MinIO volume.
local-reset:
	docker compose down -v
	rm -rf "{{DEMO_DIR}}" "{{RESTORE_DIR}}" sentra-recovery-kit.md first-plan.json

# Initialize Sentra against local MinIO.
local-init: _require-local-passphrase build local-up
	@out=$(AWS_ACCESS_KEY_ID="{{MINIO_ACCESS_KEY}}" AWS_SECRET_ACCESS_KEY="{{MINIO_SECRET_KEY}}" {{SENTRA}} init --force 2>&1); \
	status=$?; \
	printf "%s\n" "$out"; \
	if [ $status -ne 0 ]; then \
		printf "%s\n" "$out" | grep -q "already initialized" && exit 0; \
		exit $status; \
	fi

# Create demo data and take a snapshot. Override tag with: just local-backup second
local-backup tag="first": local-init
	mkdir -p "{{DEMO_DIR}}"
	test -f "{{DEMO_DIR}}/readme.txt" || echo "hello sentra" > "{{DEMO_DIR}}/readme.txt"
	AWS_ACCESS_KEY_ID="{{MINIO_ACCESS_KEY}}" AWS_SECRET_ACCESS_KEY="{{MINIO_SECRET_KEY}}" {{SENTRA}} backup "{{DEMO_DIR}}" --tag "{{tag}}"

# Run the basic local happy path: init, backup, snapshots, and check.
local-demo: local-backup
	AWS_ACCESS_KEY_ID="{{MINIO_ACCESS_KEY}}" AWS_SECRET_ACCESS_KEY="{{MINIO_SECRET_KEY}}" {{SENTRA}} snapshots
	AWS_ACCESS_KEY_ID="{{MINIO_ACCESS_KEY}}" AWS_SECRET_ACCESS_KEY="{{MINIO_SECRET_KEY}}" {{SENTRA}} check

# List local snapshots.
local-snapshots: _require-local-passphrase build local-up
	AWS_ACCESS_KEY_ID="{{MINIO_ACCESS_KEY}}" AWS_SECRET_ACCESS_KEY="{{MINIO_SECRET_KEY}}" {{SENTRA}} snapshots

# Check local repository health.
local-check: _require-local-passphrase build local-up
	AWS_ACCESS_KEY_ID="{{MINIO_ACCESS_KEY}}" AWS_SECRET_ACCESS_KEY="{{MINIO_SECRET_KEY}}" {{SENTRA}} check

# Restore a snapshot to RESTORE_DIR. Usage: just local-restore <snapshot-id>
local-restore snapshot: _require-local-passphrase build local-up
	rm -rf "{{RESTORE_DIR}}"
	AWS_ACCESS_KEY_ID="{{MINIO_ACCESS_KEY}}" AWS_SECRET_ACCESS_KEY="{{MINIO_SECRET_KEY}}" {{SENTRA}} restore "{{snapshot}}" "{{RESTORE_DIR}}" --dry-run
	AWS_ACCESS_KEY_ID="{{MINIO_ACCESS_KEY}}" AWS_SECRET_ACCESS_KEY="{{MINIO_SECRET_KEY}}" {{SENTRA}} restore "{{snapshot}}" "{{RESTORE_DIR}}" --verify

# Export non-secret local recovery notes.
local-recovery-kit: _require-local-passphrase build local-up
	AWS_ACCESS_KEY_ID="{{MINIO_ACCESS_KEY}}" AWS_SECRET_ACCESS_KEY="{{MINIO_SECRET_KEY}}" {{SENTRA}} recovery-kit --out sentra-recovery-kit.md

# Suggest .sentraignore patterns for the local demo directory.
local-advise-ignore: build
	AWS_ACCESS_KEY_ID="{{MINIO_ACCESS_KEY}}" AWS_SECRET_ACCESS_KEY="{{MINIO_SECRET_KEY}}" {{SENTRA}} agent advise-ignore "{{DEMO_DIR}}"

# Run a local-only agent scan with no LLM provider call.
local-agent: _require-local-passphrase build local-up
	AWS_ACCESS_KEY_ID="{{MINIO_ACCESS_KEY}}" AWS_SECRET_ACCESS_KEY="{{MINIO_SECRET_KEY}}" {{SENTRA}} agent scan --local-only --root "{{DEMO_DIR}}"

# Launch the TUI against local MinIO.
local-ui: _require-local-passphrase build local-up
	AWS_ACCESS_KEY_ID="{{MINIO_ACCESS_KEY}}" AWS_SECRET_ACCESS_KEY="{{MINIO_SECRET_KEY}}" {{SENTRA}} ui

# Preview retention pruning against local MinIO. This is dry-run only.
local-prune: _require-local-passphrase build local-up
	AWS_ACCESS_KEY_ID="{{MINIO_ACCESS_KEY}}" AWS_SECRET_ACCESS_KEY="{{MINIO_SECRET_KEY}}" {{SENTRA}} prune --explain

_require-local-passphrase:
	@if [ -z "$SENTRA_PASSPHRASE" ]; then \
		echo "SENTRA_PASSPHRASE is required for local Sentra recipes."; \
		echo "Run: export SENTRA_PASSPHRASE='<choose-a-local-passphrase>'"; \
		exit 1; \
	fi
