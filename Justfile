# Sentra task runner. Run `just` (no args) to list recipes.
#
# Layout:
#   Build & quality gate  — build, test, check, lint, ...
#   Local dev (MinIO)      — zero-cloud end-to-end via docker + MinIO
#   AWS helpers            — open the S3 console, reset local AWS setup state
#   Release & tooling      — goreleaser snapshot + SBOM
#
# The fast path to try the app end-to-end with no AWS account:
#   just local          # builds, starts MinIO, opens the TUI (first-run wizard)
#   just local-reset    # wipe everything and start fresh

set dotenv-load := true

GO         := env_var_or_default("GO", "go")
BIN        := "bin/sentra"
SENTRA     := "./bin/sentra"
PKG        := "./..."
CHECK_DIRS := "cmd internal"

# Local MinIO (docker-compose.yaml) credentials + demo paths.
MINIO_ACCESS_KEY := env_var_or_default("MINIO_ROOT_USER", "minioadmin")
MINIO_SECRET_KEY := env_var_or_default("MINIO_ROOT_PASSWORD", "minioadmin")
DEMO_DIR         := env_var_or_default("SENTRA_DEMO_DIR", "demo-data")
RESTORE_DIR      := env_var_or_default("SENTRA_RESTORE_DIR", "/tmp/sentra-restored")
# Prefix that points the sentra binary at local MinIO via the AWS SDK env chain.
LOCAL_ENV := "AWS_ACCESS_KEY_ID=" + MINIO_ACCESS_KEY + " AWS_SECRET_ACCESS_KEY=" + MINIO_SECRET_KEY

RELEASE_SKIP := "publish,sign,sbom,docker"
SBOM_OUT     := "dist/sentra-source-sbom.spdx.json"

# Show the available recipes.
default:
	@just --list

# ---------------------------------------------------------------------------
# Build & quality gate
# ---------------------------------------------------------------------------

# Compile the sentra binary to bin/sentra.
build:
	{{GO}} build -o {{BIN}} ./cmd/sentra

# Run the standard local quality gate (mirrors CI).
check: build test vet lint vuln _tidy-check _fmt-check _diff-check

# Run the full quality, security, and release-tooling gate.
full-check: check release-local

# Race-detector unit tests with coverage.
test:
	{{GO}} test -race -coverprofile=coverage.out {{PKG}}

# Integration tests (needs Docker; testcontainers + MinIO).
integration:
	{{GO}} test -race -tags=integration {{PKG}}

# Lint with golangci-lint.
lint: _require-golangci-lint
	golangci-lint run

# Scan for known vulnerabilities.
vuln: _require-govulncheck
	govulncheck {{PKG}}

# Format the tree.
fmt:
	{{GO}} fmt {{PKG}}

# Vet the tree.
vet:
	{{GO}} vet {{PKG}}

# Tidy go.mod / go.sum.
tidy:
	{{GO}} mod tidy

# Remove build artifacts.
clean:
	rm -rf bin coverage.out

# ---------------------------------------------------------------------------
# Local dev with MinIO (zero-cloud, no AWS account)
# ---------------------------------------------------------------------------

# Build + open the TUI against local MinIO (starts MinIO; first run = wizard). The easy path.
local: build
	{{SENTRA}} local

# Print the environment exports used by the granular local-* recipes.
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

# Completely reset the local test env (MinIO volume, sentra-local config + keyring, demo data) so the next run starts fresh at the first-run wizard.
local-reset:
	docker compose down -v
	rm -rf "{{DEMO_DIR}}" "{{RESTORE_DIR}}" sentra-recovery-kit.md first-plan.json
	rm -rf .sentra-local.yaml .sentra-local
	-security delete-generic-password -s sentra -a sentra-test >/dev/null 2>&1

# Initialize Sentra against local MinIO (non-interactive; needs SENTRA_PASSPHRASE).
local-init: _require-local-passphrase build local-up
	@out=$({{LOCAL_ENV}} {{SENTRA}} init --force 2>&1); \
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
	{{LOCAL_ENV}} {{SENTRA}} backup "{{DEMO_DIR}}" --tag "{{tag}}"

# Run the basic local happy path: init, backup, snapshots, and check.
local-demo: local-backup
	{{LOCAL_ENV}} {{SENTRA}} snapshots
	{{LOCAL_ENV}} {{SENTRA}} check

# List local snapshots.
local-snapshots: _require-local-passphrase build local-up
	{{LOCAL_ENV}} {{SENTRA}} snapshots

# Check local repository health.
local-check: _require-local-passphrase build local-up
	{{LOCAL_ENV}} {{SENTRA}} check

# Restore a snapshot to RESTORE_DIR. Usage: just local-restore <snapshot-id>
local-restore snapshot: _require-local-passphrase build local-up
	rm -rf "{{RESTORE_DIR}}"
	{{LOCAL_ENV}} {{SENTRA}} restore "{{snapshot}}" "{{RESTORE_DIR}}" --dry-run
	{{LOCAL_ENV}} {{SENTRA}} restore "{{snapshot}}" "{{RESTORE_DIR}}" --verify

# Preview retention pruning against local MinIO (dry-run only).
local-prune: _require-local-passphrase build local-up
	{{LOCAL_ENV}} {{SENTRA}} prune --explain

# Export non-secret local recovery notes.
local-recovery-kit: _require-local-passphrase build local-up
	{{LOCAL_ENV}} {{SENTRA}} recovery-kit --out sentra-recovery-kit.md

# Suggest .sentraignore patterns for the local demo directory.
local-advise-ignore: build
	{{LOCAL_ENV}} {{SENTRA}} agent advise-ignore "{{DEMO_DIR}}"

# Run a local-only agent scan with no LLM provider call.
local-agent: _require-local-passphrase build local-up
	{{LOCAL_ENV}} {{SENTRA}} agent scan --local-only --root "{{DEMO_DIR}}"

# Launch the TUI against local MinIO using an explicit sentra.yaml + env.
local-ui: _require-local-passphrase build local-up
	{{LOCAL_ENV}} {{SENTRA}} ui

# ---------------------------------------------------------------------------
# AWS helpers
# ---------------------------------------------------------------------------

# Open the configured AWS S3 bucket in the browser. Override with: just aws-open-bucket <bucket> <region>
aws-open-bucket bucket="" region="" config="sentra.yaml":
	@set -eu; \
	cfg="{{config}}"; \
	bucket="{{bucket}}"; \
	region="{{region}}"; \
	prefix=""; \
	read_yaml_value() { \
		key="$1"; \
		file="$2"; \
		awk -v want="$key" ' \
			/^[[:space:]]*repo:/ { in_repo=1; next } \
			in_repo && /^[^[:space:]]/ { in_repo=0; in_s3=0 } \
			in_repo && /^[[:space:]]*s3:/ { in_s3=1; next } \
			in_s3 && /^[[:space:]]{2}[^[:space:]]/ && $1 != "s3:" { in_s3=0 } \
			in_s3 { \
				line=$0; \
				sub("^[[:space:]]*" want ":[[:space:]]*", "", line); \
				if (line != $0) { \
					sub("[[:space:]]+#.*$", "", line); \
					gsub(/^\"|\"$/, "", line); \
					print line; \
					exit; \
				} \
			} \
		' "$file"; \
	}; \
	if [ -f "$cfg" ]; then \
		if [ -z "$bucket" ]; then \
			bucket="$(read_yaml_value bucket "$cfg")"; \
			prefix="$(read_yaml_value prefix "$cfg")"; \
		fi; \
		if [ -z "$region" ]; then region="$(read_yaml_value region "$cfg")"; fi; \
	fi; \
	if [ -z "$bucket" ]; then \
		echo "No S3 bucket found. Run setup first or pass one: just aws-open-bucket <bucket> <region>"; \
		exit 1; \
	fi; \
	if [ -z "$region" ]; then region="us-east-1"; fi; \
	url="$(python3 -c 'import sys; from urllib.parse import urlencode; bucket, region, prefix = sys.argv[1:4]; query = {"region": region, "bucketType": "general", "tab": "objects"}; query.update({"prefix": prefix} if prefix else {}); print(f"https://s3.console.aws.amazon.com/s3/buckets/{bucket}?{urlencode(query)}")' "$bucket" "$region" "$prefix")"; \
	echo "Opening $url"; \
	if command -v open >/dev/null 2>&1; then \
		open "$url"; \
	elif command -v xdg-open >/dev/null 2>&1; then \
		xdg-open "$url"; \
	elif command -v powershell.exe >/dev/null 2>&1; then \
		powershell.exe -NoProfile -Command "Start-Process '$url'"; \
	else \
		echo "$url"; \
	fi

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

# ---------------------------------------------------------------------------
# Release & security tooling
# ---------------------------------------------------------------------------

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

# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------

_tidy-check:
	{{GO}} mod tidy -diff

_fmt-check:
	test -z "$(gofmt -l {{CHECK_DIRS}})"

_diff-check:
	git diff --check

_require-local-passphrase:
	@if [ -z "$SENTRA_PASSPHRASE" ]; then \
		echo "SENTRA_PASSPHRASE is required for the granular local-* recipes."; \
		echo "Run: export SENTRA_PASSPHRASE='<choose-a-local-passphrase>'"; \
		echo "(Tip: 'just local' uses the wizard and needs no env passphrase.)"; \
		exit 1; \
	fi

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
