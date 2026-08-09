# List available recipes.
default:
    @just --list

# Build the standalone webfetch binary.
build:
    go build -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o webfetch ./cmd/webfetch/

# Run the full test suite with the race detector.
test:
    go test -race ./...

# Remove the local binary.
clean:
    rm -f ./webfetch
