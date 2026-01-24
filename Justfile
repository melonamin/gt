# gt justfile - commands for development

# Default recipe: run all tests
default: test

# Run all tests
test:
    go test -v ./...

# Run unit tests only (pure functions, no git repos)
test-unit:
    go test -v -run 'TestValidate|TestTruncate|TestFormat|TestFilter|TestGetShell|TestParseArgs|TestConfigDefault' ./...

# Run integration tests only (requires real git repos)
test-integration:
    go test -v -run 'TestGetAhead|TestListWorktrees|TestRef|TestLocalBranch|TestGetDefault|TestCanFast|TestLoad|TestSave|TestEnsure|TestConfig' ./...

# Run e2e tests only (builds and runs the binary)
test-e2e:
    go test -v -run 'TestGT' ./...

# Run tests with race detection
test-race:
    go test -race -v ./...

# Generate coverage report
test-coverage:
    go test -v -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out -o coverage.html
    @echo "Coverage report generated: coverage.html"

# Show coverage percentage
coverage:
    go test -cover ./...

# Build the binary
build:
    go build -o gt .

# Build with version info
build-release:
    go build -ldflags="-s -w" -o gt .

# Run linter
lint:
    golangci-lint run

# Format code
fmt:
    go fmt ./...

# Vet code
vet:
    go vet ./...

# Clean build artifacts
clean:
    rm -f gt coverage.out coverage.html

# Run all checks (format, vet, lint, test)
check: fmt vet test

# Install binary to GOPATH/bin
install:
    go install .
