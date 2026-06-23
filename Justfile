GO := env_var_or_default("GO", "go")
BIN := "bin/sentra"
PKG := "./..."

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
