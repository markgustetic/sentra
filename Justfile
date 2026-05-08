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
	golangci-lint run

fmt:
	{{GO}} fmt {{PKG}}

vet:
	{{GO}} vet {{PKG}}

tidy:
	{{GO}} mod tidy

clean:
	rm -rf bin coverage.out
