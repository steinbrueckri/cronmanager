# Add mise-managed tools to PATH
export PATH := `mise bin-paths 2>/dev/null | tr '\n' ':' || echo ""` + env_var('PATH')

# Default recipe to display help
default:
    @just --list

# Build the binary for the current platform
build:
    @echo "Building cronmanager..."
    go build -o cronmanager main.go
    @echo "✓ Build complete: ./cronmanager"

# Run tests
test:
    @echo "Running tests..."
    @go test -v ./...
    @echo "✓ Tests complete"

# Run tests with coverage
test-coverage:
    @echo "Running tests with coverage..."
    @go test -v -coverprofile=coverage.out ./...
    @go tool cover -html=coverage.out -o coverage.html
    @echo "✓ Coverage report: coverage.html"

# Format code
fmt:
    @echo "Formatting code..."
    @go fmt ./...
    @echo "✓ Code formatted"

# Run go vet
vet:
    @echo "Running go vet..."
    @go vet ./...
    @echo "✓ Vet complete"

# Run golangci-lint
lint:
    @echo "Running golangci-lint..."
    @golangci-lint run
    @echo "✓ Lint complete"

# Run staticcheck
staticcheck:
    @echo "Running staticcheck..."
    @staticcheck ./...
    @echo "✓ Staticcheck complete"

# Lint Markdown files
markdown-lint:
    @echo "Linting Markdown files..."
    @markdownlint-cli2 "*.md"
    @echo "✓ Markdown lint complete"

# Run all checks
check: fmt vet lint staticcheck markdown-lint test
    @echo "✓ All checks passed"

# CI pipeline - runs all checks including release validation
ci: fmt vet lint staticcheck markdown-lint test validate-release
    @echo "✓ CI Pipeline Complete"

# Clean build artifacts
clean:
    @echo "Cleaning..."
    @rm -rf cronmanager coverage.out coverage.html dist/
    @echo "✓ Clean complete"

# Install the binary to /usr/local/bin
install: build
    @echo "Installing to /usr/local/bin..."
    @sudo mv cronmanager /usr/local/bin/
    @echo "✓ Installed"

# Update dependencies
deps:
    @go mod tidy
    @go mod download

# Install development tools
setup:
    @echo "Installing development tools..."
    @command -v mise >/dev/null || (echo "Error: mise not installed. Install with: brew install mise" && exit 1)
    @mise install
    @echo "✓ Development tools installed"

# Build snapshot with GoReleaser (optional, requires goreleaser)
release-snapshot:
    @echo "Building snapshot..."
    @goreleaser build --snapshot --clean
    @echo "✓ Snapshot built in dist/"

# Validate GoReleaser configuration
validate-release:
    @echo "Validating GoReleaser config..."
    @goreleaser check || (echo "Error: goreleaser not found. Run: just setup" && exit 1)
    @echo "✓ GoReleaser config valid"

# Update version in main.go
set-version VERSION:
    @echo "Updating version to {{VERSION}}..."
    @sed -i '' 's/version = ".*"/version = "{{VERSION}}"/' main.go
    @echo "✓ Version updated in main.go"

# Create a new release (updates version, commits, tags, builds)
release TAG:
    @echo "Creating release {{TAG}}..."
    @just set-version $(echo {{TAG}} | sed 's/^v//')
    @git add main.go
    @git commit -m "Release {{TAG}}" || true
    @git push
    @git tag -a {{TAG}} -m "Release {{TAG}}"
    @echo "Building and publishing release..."
    @GITHUB_TOKEN=$(op item get "GitHub Personal Access Token" --field "token" --reveal --account pixel-combo.1password.com) goreleaser release --clean
    @echo "✓ Release {{TAG}} published to GitHub"
    @echo "Don't forget: git push origin {{TAG}}"