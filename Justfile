set dotenv-load := true

GO := env_var_or_default("GO", "go")
BIN := "bin/sentra"
PKG := "./..."
CHECK_DIRS := "cmd internal"
SENTRA := "./bin/sentra"
MINIO_ACCESS_KEY := env_var_or_default("MINIO_ROOT_USER", "minioadmin")
MINIO_SECRET_KEY := env_var_or_default("MINIO_ROOT_PASSWORD", "minioadmin")
DEMO_DIR := env_var_or_default("SENTRA_DEMO_DIR", "demo-data")
RESTORE_DIR := env_var_or_default("SENTRA_RESTORE_DIR", "/tmp/sentra-restored")
RELEASE_SKIP := "publish,sign,sbom,docker"
SBOM_OUT := "dist/sentra-source-sbom.spdx.json"

build:
	{{GO}} build -o {{BIN}} ./cmd/sentra

# Run the standard local quality gate.
check: build test vet lint vuln _tidy-check _fmt-check _diff-check

# Run the full quality, security, and release-tooling gate.
full-check: check release-local

test:
	{{GO}} test -race -coverprofile=coverage.out {{PKG}}

integration:
	{{GO}} test -race -tags=integration {{PKG}}

lint: _require-golangci-lint
	golangci-lint run

vuln: _require-govulncheck
	govulncheck {{PKG}}

fmt:
	{{GO}} fmt {{PKG}}

vet:
	{{GO}} vet {{PKG}}

tidy:
	{{GO}} mod tidy

clean:
	rm -rf bin coverage.out

# Reset local Sentra/AWS CLI profile state for AWS setup testing. Does not delete S3 buckets or objects.
aws-reset profile="sentra" config="sentra.yaml": build
	@set -eu; \
	if [ -z "{{profile}}" ]; then \
		echo "AWS profile cannot be blank for aws-reset."; \
		exit 1; \
	fi; \
	cfg="{{config}}"; \
	draft="$(dirname "$cfg")/.$(basename "$cfg").setup-draft"; \
	echo "This will reset local AWS setup test state:"; \
	echo "  sentra config: $cfg"; \
	echo "  setup draft:   $draft"; \
	echo "  AWS profile:   {{profile}}"; \
	echo; \
	echo "It will remove Sentra's saved OS-keyring passphrase for this config if one exists."; \
	echo "It will not delete S3 buckets, S3 objects, AWS accounts, or global AWS SSO/browser-login caches."; \
	printf "Type 'reset' to continue: "; \
	if ! IFS= read -r answer; then \
		echo "Canceled."; \
		exit 1; \
	fi; \
	if [ "$answer" != "reset" ]; then \
		echo "Canceled."; \
		exit 1; \
	fi; \
	{{SENTRA}} password forget --config "$cfg"; \
	rm -f -- "$cfg" "$draft"; \
	if command -v aws >/dev/null 2>&1; then \
		for key in \
			aws_access_key_id \
			aws_secret_access_key \
			aws_session_token \
			credential_process \
			output \
			region \
			role_arn \
			sso_account_id \
			sso_region \
			sso_role_name \
			sso_session \
			sso_start_url \
			source_profile \
			web_identity_token_file; do \
			aws configure unset "$key" --profile "{{profile}}" >/dev/null 2>&1 || true; \
		done; \
		echo "Cleared AWS CLI config/credential keys for profile '{{profile}}'."; \
	else \
		echo "AWS CLI not found; skipped profile cleanup."; \
	fi; \
	echo "Done. If AWS_PROFILE or AWS_ACCESS_KEY_ID are exported in your shell, unset them before rerunning setup."

# Verify optional security/release tooling is installed.
tools: _require-govulncheck _require-goreleaser _require-cosign _require-syft
	@for tool in govulncheck goreleaser cosign syft; do \
		printf "%-12s %s\n" "$tool" "$(command -v "$tool")"; \
	done

# Build local release artifacts and a source-tree SBOM under dist/.
release-local: tools _release-check _release-snapshot _sbom

_release-check: _require-goreleaser
	goreleaser check

_release-snapshot: _require-goreleaser
	goreleaser release --snapshot --clean --skip={{RELEASE_SKIP}}

_sbom: _require-syft
	mkdir -p dist
	syft dir:. --exclude './dist/**' -o spdx-json={{SBOM_OUT}}

_tidy-check:
	{{GO}} mod tidy -diff

_fmt-check:
	test -z "$(gofmt -l {{CHECK_DIRS}})"

_diff-check:
	git diff --check

_require-golangci-lint:
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "golangci-lint is required."; \
		echo "Install it with: brew install golangci-lint"; \
		echo "Or: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
		exit 127; \
	fi

_require-govulncheck:
	@if ! command -v govulncheck >/dev/null 2>&1; then \
		echo "govulncheck is required."; \
		echo "Install it with: brew install govulncheck"; \
		echo "Or: go install golang.org/x/vuln/cmd/govulncheck@latest"; \
		exit 127; \
	fi

_require-goreleaser:
	@if ! command -v goreleaser >/dev/null 2>&1; then \
		echo "goreleaser is required."; \
		echo "Install it with: brew install goreleaser"; \
		exit 127; \
	fi

_require-cosign:
	@if ! command -v cosign >/dev/null 2>&1; then \
		echo "cosign is required."; \
		echo "Install it with: brew install cosign"; \
		exit 127; \
	fi

_require-syft:
	@if ! command -v syft >/dev/null 2>&1; then \
		echo "syft is required."; \
		echo "Install it with: brew install syft"; \
		exit 127; \
	fi

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
