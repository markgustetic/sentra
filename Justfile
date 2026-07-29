# Sentra task runner. Run `just` (no args) to list recipes.
#
# Layout:
#   Build & quality gate  — build, test, check, lint, ...
#   Local dev (MinIO)      — zero-cloud end-to-end via docker + MinIO
#   AWS helpers            — run against real S3, smoke-test it, reset, open console
#   Release & tooling      — goreleaser snapshot + SBOM
#
# The fast path to try the app end-to-end with no AWS account:
#   just local          # builds, starts MinIO, opens the TUI (first-run wizard)
#   just local-reset    # wipe everything and start fresh
#
# Testing against real AWS S3:
#   just aws            # builds + runs `sentra`; first run opens the setup wizard
#   just aws-smoke      # non-interactive backup→restore→dedup→check (after setup)
#   just aws-reset      # wipe local state so the next `just aws` is a fresh wizard

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
# AWS smoke-test payload + restore target (real S3; kept distinct from the MinIO demo dirs).
AWS_DEMO_DIR     := env_var_or_default("SENTRA_AWS_DEMO_DIR", "aws-test-data")
AWS_RESTORE_DIR  := env_var_or_default("SENTRA_AWS_RESTORE_DIR", "/tmp/sentra-aws-restored")
# Size of the dedup fixture. Must span many FastCDC chunks (avg 1 MiB, max
# 4 MiB) or appending to it re-cuts the file's only chunk and the incremental
# backup has nothing to dedup.
AWS_FIXTURE_MIB  := env_var_or_default("SENTRA_AWS_FIXTURE_MIB", "16")

# `.env` belongs to the MinIO flow, but `set dotenv-load` above injects its
# SENTRA_PASSPHRASE into *every* recipe — including the AWS ones, where it is
# simply the wrong secret and fails the unlock with "repo: wrong passphrase".
# config.Resolve checks the env var before the OS keyring, so the AWS recipes
# have to clear it to reach the passphrase the setup wizard saved. Set
# SENTRA_AWS_PASSPHRASE to supply one explicitly instead (CI, or no keyring).
#
# The value moves through the environment and never onto a command line, so it
# stays out of `just`'s echoed recipe lines and `just --evaluate`.
AWS_PASSPHRASE_ENV := '''
	if [ -n "${SENTRA_AWS_PASSPHRASE:-}" ]; then
		export SENTRA_PASSPHRASE="$SENTRA_AWS_PASSPHRASE"
	else
		unset SENTRA_PASSPHRASE
	fi
'''

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

# Install `sentra` to your Go bin (go env GOBIN, else GOPATH/bin) so it runs by name.
install:
	{{GO}} install ./cmd/sentra
	@bindir="$({{GO}} env GOBIN)"; [ -n "$bindir" ] || bindir="$({{GO}} env GOPATH)/bin"; echo "Installed sentra -> $bindir/sentra (ensure $bindir is on your PATH)"

# Run the standard local quality gate (mirrors CI).
check: build test vet lint vuln _tidy-check _fmt-check _diff-check commits-build

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

# Reinstall `sentra`, then completely reset the local test env (MinIO volume, sentra-local config + keyring, demo data) so the next run starts fresh at the first-run wizard.
local-reset: install
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

# Build + run sentra against ./sentra.yaml: first run (no config, e.g. after `just aws-reset`) opens the AWS setup wizard; once configured it opens the dashboard. Same as plain `sentra` after `just install`. Needs a real terminal.
aws: build
	#!/usr/bin/env bash
	set -euo pipefail
	{{AWS_PASSPHRASE_ENV}}
	{{SENTRA}}

# Create the AWS test payload under AWS_DEMO_DIR: two small text files plus the dedup fixture.
aws-seed:
	#!/usr/bin/env bash
	set -euo pipefail
	mkdir -p "{{AWS_DEMO_DIR}}"
	test -f "{{AWS_DEMO_DIR}}/readme.txt" || printf 'hello sentra (aws)\n' > "{{AWS_DEMO_DIR}}/readme.txt"
	test -f "{{AWS_DEMO_DIR}}/notes.md"   || printf '# notes\nsecond file, unchanged across snapshots\n' > "{{AWS_DEMO_DIR}}/notes.md"
	# The fixture is regenerated on every run, and deliberately NOT idempotent:
	# a re-run against the same repo would otherwise dedup the whole thing away
	# on the *first* snapshot, leaving the incremental with nothing to prove.
	# Fresh bytes make snapshot 1 genuinely new on every run.
	#
	# /dev/urandom, not compressible filler: `new_bytes` is measured after zstd,
	# so compressible data would collapse it toward zero and the dedup assertion
	# below would pass whether or not a single chunk was actually reused.
	dd if=/dev/urandom of="{{AWS_DEMO_DIR}}/dedup-fixture.bin" \
		bs=1048576 count="{{AWS_FIXTURE_MIB}}" status=none

# Verify a configured AWS repo without changing anything (identity, bucket, repo).
aws-doctor: build
	#!/usr/bin/env bash
	set -euo pipefail
	{{AWS_PASSPHRASE_ENV}}
	{{SENTRA}} doctor

# Non-interactive AWS test: backup → restore+verify → incremental (proves dedup) → check. Needs a configured sentra.yaml (run `just aws` first) and a resolvable passphrase (the keyring entry from setup, or SENTRA_AWS_PASSPHRASE). Reads and writes S3 but never deletes it.
aws-smoke: build aws-seed
	#!/usr/bin/env bash
	set -euo pipefail
	{{AWS_PASSPHRASE_ENV}}
	cfg="sentra.yaml"
	if [ ! -f "$cfg" ]; then
	  echo "No $cfg found. Run 'just aws' and complete the setup wizard first."
	  exit 1
	fi
	command -v jq >/dev/null 2>&1 || { echo "jq is required for aws-smoke (brew install jq)."; exit 1; }

	# Latest snapshot's uploaded-vs-read bytes, as "<new_bytes> <bytes>".
	snap_bytes() { {{SENTRA}} snapshots --json | jq -r 'sort_by(.created_at) | last | "\(.new_bytes) \(.bytes)"'; }

	echo "==> doctor"
	{{SENTRA}} doctor

	echo "==> backup (full)"
	{{SENTRA}} backup "{{AWS_DEMO_DIR}}" --tag aws-smoke
	stats="$(snap_bytes)"; full_new="${stats% *}"; full_total="${stats#* }"
	# Sanity-check the fixture before trusting the dedup number further down. A
	# full backup of fresh random bytes should upload about what it read; if it
	# doesn't, the payload is compressible or already in the repo, and the
	# incremental assertion would pass without proving a thing.
	if [ "$full_new" -lt $(( full_total / 2 )) ]; then
	  echo "FAIL: full backup uploaded new=$full_new of total=$full_total."
	  echo "The fixture is not fresh incompressible data, so the dedup check below would be vacuous."
	  exit 1
	fi
	echo "full backup uploaded new=$full_new total=$full_total (expected new ~= total)"

	echo "==> snapshots"
	{{SENTRA}} snapshots
	snap="$({{SENTRA}} snapshots --json | jq -r 'sort_by(.created_at) | last | .id')"
	if [ -z "$snap" ] || [ "$snap" = "null" ]; then echo "no snapshot found after backup"; exit 1; fi
	echo "latest snapshot: $snap"

	echo "==> restore + verify"
	rm -rf -- "{{AWS_RESTORE_DIR}}"
	{{SENTRA}} restore "$snap" "{{AWS_RESTORE_DIR}}" --verify
	# restore writes paths relative to the backup root, so the trees compare flat.
	diff -r "{{AWS_DEMO_DIR}}" "{{AWS_RESTORE_DIR}}"
	echo "restore is exact-byte OK"

	echo "==> incremental backup (dedup)"
	# Append at EOF. FastCDC cut points depend only on the bytes preceding them,
	# so every chunk but the last keeps its hash and only the tail is re-uploaded.
	printf 'appended at %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "{{AWS_DEMO_DIR}}/dedup-fixture.bin"
	{{SENTRA}} backup "{{AWS_DEMO_DIR}}" --tag aws-smoke-2
	stats="$(snap_bytes)"; inc_new="${stats% *}"; inc_total="${stats#* }"
	# Worst case is one max-size (4 MiB) tail chunk against the 16 MiB fixture,
	# so half is a wide margin. Broken dedup re-uploads everything and lands
	# near 100%, well clear of the threshold in the other direction.
	limit=$(( inc_total / 2 ))
	if [ "$inc_new" -ge "$limit" ]; then
	  echo "FAIL: incremental uploaded new=$inc_new of total=$inc_total, expected < $limit."
	  echo "Content-defined dedup did not reuse the unchanged chunks."
	  exit 1
	fi
	echo "incremental uploaded new=$inc_new total=$inc_total (< $limit) — dedup reused the unchanged chunks"

	echo "==> check"
	{{SENTRA}} check
	echo "AWS smoke test complete."

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
	echo "  test data:     {{AWS_DEMO_DIR}}"; \
	echo "  restore dir:   {{AWS_RESTORE_DIR}}"; \
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
	{{SENTRA}} password forget --config "$cfg" || true; \
	rm -f -- "$cfg" "$draft"; \
	rm -rf -- "{{AWS_DEMO_DIR}}" "{{AWS_RESTORE_DIR}}"; \
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
	echo "Done. Run 'just aws' to start fresh at the setup wizard."; \
	echo "(If AWS_PROFILE or AWS_ACCESS_KEY_ID are exported in your shell, unset them first.)"

# DESTRUCTIVE: delete every object under the repo's S3 prefix (read from sentra.yaml) so setup re-inits an empty repo — `aws-reset` only clears LOCAL state, leaving the encrypted repo in S3. Needs the AWS CLI + credentials; refuses an empty prefix; confirm by typing the bucket name.
aws-s3-empty config="sentra.yaml":
	#!/usr/bin/env bash
	set -euo pipefail
	cfg="{{config}}"
	command -v aws >/dev/null 2>&1 || { echo "AWS CLI not found; install it or empty the prefix from the S3 console."; exit 1; }
	[ -f "$cfg" ] || { echo "No $cfg found — nothing to read bucket/prefix from."; exit 1; }
	read_s3() {
	  awk -v want="$1" '
	    /^[[:space:]]*repo:/ { in_repo=1; next }
	    in_repo && /^[^[:space:]]/ { in_repo=0; in_s3=0 }
	    in_repo && /^[[:space:]]*s3:/ { in_s3=1; next }
	    in_s3 && /^[[:space:]][[:space:]][^[:space:]]/ && $1 != "s3:" { in_s3=0 }
	    in_s3 {
	      line=$0
	      sub("^[[:space:]]*" want ":[[:space:]]*", "", line)
	      if (line != $0) { sub("[[:space:]]+#.*$", "", line); gsub(/^"|"$/, "", line); print line; exit }
	    }
	  ' "$cfg"
	}
	bucket="$(read_s3 bucket)"
	prefix="$(read_s3 prefix)"
	[ -n "$bucket" ] || { echo "Could not read repo.s3.bucket from $cfg."; exit 1; }
	[ -n "$prefix" ] || { echo "repo.s3.prefix is empty in $cfg — refusing (would target the whole bucket)."; exit 1; }
	case "$prefix" in */) ;; *) prefix="$prefix/";; esac
	echo "DESTRUCTIVE: this permanently deletes every object under:"
	echo "  s3://$bucket/$prefix"
	echo "It does NOT delete the bucket itself or anything outside that prefix."
	printf "Type the bucket name '%s' to continue: " "$bucket"
	IFS= read -r answer || { echo "Canceled."; exit 1; }
	[ "$answer" = "$bucket" ] || { echo "Canceled."; exit 1; }
	aws s3 rm "s3://$bucket/$prefix" --recursive
	echo "Emptied s3://$bucket/$prefix. Run 'just aws-reset' next, then 'just aws' for a clean setup."

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

# Verify every commit in <base>..HEAD compiles on its own.
#
# A gate builds the working TREE, never your commits. The two diverge exactly
# when the tree is dirty — which is when you are most likely to `git add` a file
# carrying a hunk you did not write. That is how three non-building commits once
# reached main while every check reported success. `git bisect` is what this
# protects.
commits-build base="origin/main":
	#!/usr/bin/env bash
	set -euo pipefail
	if ! git rev-parse --verify -q "{{base}}" >/dev/null; then
	  echo "base {{base}} not found — skipping"
	  exit 0
	fi
	commits="$(git rev-list --reverse "{{base}}..HEAD")"
	if [ -z "$commits" ]; then
	  echo "no commits in {{base}}..HEAD — nothing to check"
	  exit 0
	fi
	tmp="$(mktemp -d)"
	wt="$tmp/wt"
	cleanup() { git worktree remove --force "$wt" >/dev/null 2>&1 || true; rm -rf "$tmp"; }
	trap cleanup EXIT
	rc=0
	for sha in $commits; do
	  subject="$(git log -1 --format=%s "$sha")"
	  git worktree add -q --detach "$wt" "$sha"
	  if (cd "$wt" && {{GO}} build ./... >/dev/null 2>&1); then
	    printf 'ok     %s  %s\n' "$(git rev-parse --short "$sha")" "$subject"
	  else
	    printf 'BROKEN %s  %s\n' "$(git rev-parse --short "$sha")" "$subject"
	    rc=1
	  fi
	  git worktree remove --force "$wt"
	done
	exit $rc

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
