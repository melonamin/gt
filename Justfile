#!/usr/bin/env just

# Default recipe
default:
    @just --list

# Build the application
build:
    go build -o gt main.go

# Run the application
run:
    go run main.go

# Install the application to GOPATH/bin
install:
    go install

# Clean build artifacts
clean:
    rm -f gt gt-* *.zip
    rm -rf dist/

# Format code
fmt:
    go fmt ./...

# Run go vet
vet:
    go vet ./...

# Update dependencies
deps:
    go mod tidy
    go mod download

# Build for multiple platforms
build-all: build-linux build-darwin

# Build for Linux (amd64 and arm64)
build-linux:
    @echo "Building for Linux (amd64)..."
    GOOS=linux GOARCH=amd64 go build -o gt-linux-amd64 main.go
    @echo "Building for Linux (arm64)..."
    GOOS=linux GOARCH=arm64 go build -o gt-linux-arm64 main.go

# Build for macOS (amd64 and arm64)
build-darwin:
    GOOS=darwin GOARCH=amd64 go build -o gt-darwin-amd64 main.go
    GOOS=darwin GOARCH=arm64 go build -o gt-darwin-arm64 main.go

# Build for macOS (universal binary)
build-macos:
    @echo "Building for macOS (amd64)..."
    GOOS=darwin GOARCH=amd64 go build -o gt-darwin-amd64 main.go
    @echo "Building for macOS (arm64)..."
    GOOS=darwin GOARCH=arm64 go build -o gt-darwin-arm64 main.go
    @echo "Creating universal binary..."
    lipo -create -output gt gt-darwin-amd64 gt-darwin-arm64
    rm gt-darwin-amd64 gt-darwin-arm64
    @echo "Universal binary created: gt"

# Code sign the macOS binary
sign: build-macos
    @echo "Code signing binary..."
    codesign --force --options runtime --sign "Developer ID Application: Ameba Labs, LLC (X93LWC49WV)" --timestamp gt
    @echo "Verifying signature..."
    codesign -dv --verbose=4 gt

# Create zip archive for notarization
package: sign
    @echo "Creating zip archive..."
    zip -r gt.zip gt
    @echo "Archive created at gt.zip"

# Submit for notarization
notarize: package
    @echo "Submitting for notarization..."
    xcrun notarytool submit gt.zip \
        --keychain-profile "notarytool-kefir" \
        --wait

# Verify notarization
verify-notarization: notarize
    @echo "Verifying notarization..."
    @echo "Note: Standalone binaries cannot be stapled, but they are still notarized"
    @echo "Extracting binary from zip..."
    unzip -o gt.zip
    @echo "Checking notarization status..."
    spctl -a -vvv -t install gt 2>&1 || true
    @echo "Binary is ready for distribution!"

# Create distribution archives
dist-macos: verify-notarization
    @echo "Creating macOS distribution archives..."
    mkdir -p dist
    # Universal binary
    cp gt dist/gt-macos-universal
    cd dist && zip -r gt-macos-universal.zip gt-macos-universal
    cd dist && shasum -a 256 gt-macos-universal.zip > gt-macos-universal.zip.sha256
    rm dist/gt-macos-universal
    @echo "macOS distribution ready in dist/"

# Build Linux distributions
dist-linux: build-linux
    @echo "Creating Linux distribution archives..."
    mkdir -p dist
    # Linux amd64
    cp gt-linux-amd64 dist/
    cd dist && tar czf gt-linux-amd64.tar.gz gt-linux-amd64
    cd dist && shasum -a 256 gt-linux-amd64.tar.gz > gt-linux-amd64.tar.gz.sha256
    rm dist/gt-linux-amd64
    # Linux arm64
    cp gt-linux-arm64 dist/
    cd dist && tar czf gt-linux-arm64.tar.gz gt-linux-arm64
    cd dist && shasum -a 256 gt-linux-arm64.tar.gz > gt-linux-arm64.tar.gz.sha256
    rm dist/gt-linux-arm64
    @echo "Linux distributions ready in dist/"

# Full release flow for all platforms
release: dist-macos dist-linux
    @echo "Release build complete!"
    @echo "Distribution files ready in dist/"
    @ls -lh dist/