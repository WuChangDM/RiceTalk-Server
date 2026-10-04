.PHONY: build run dev test migrate clean fmt vet lint

BINARY_NAME=ridgericetalk
BUILD_DIR=./build
MAIN_FILE=cmd/server/main.go

# Build the server binary (output to build dir)
build:
	CGO_ENABLED=1 CC=gcc go build -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/server

# Run the server (development)
run:
	CGO_ENABLED=1 CC=gcc go run ./cmd/server

# Run with hot reload (requires air)
dev:
	air -c .air.toml

# Run tests
test:
	go test -v -race ./...

# Run database migrations
migrate:
	go run cmd/migrate/main.go up

# Rollback last migration
migrate-down:
	go run cmd/migrate/main.go down

# Clean build artifacts
clean:
	rm -rf $(BUILD_DIR)
	rm -f $(BINARY_NAME)
	rm -f $(BINARY_NAME).exe
	rm -f ./storage/*.db
	# Legacy binaries in parent directory (backward compatibility)
	rm -f ../$(BINARY_NAME)
	rm -f ../$(BINARY_NAME).exe

# Format code
fmt:
	go fmt ./...

# Vet code
vet:
	go vet ./...

# Install dependencies
deps:
	go mod download
	go mod tidy

# Build for production (linux amd64)
build-linux:
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/server

# Build for Windows amd64
build-windows:
	CGO_ENABLED=1 CC=gcc GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o $(BUILD_DIR)/$(BINARY_NAME).exe ./cmd/server
