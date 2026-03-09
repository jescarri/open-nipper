.PHONY: build test clean cover cover-html help docker-build docker-release

# Binary output directory
BINARY_DIR=bin
BINARY_NAME=nipper

# Test output directory
COVER_DIR=coverage

# Docker configuration
IMAGE_NAME=nipper
IMAGE_TAG?=latest
DOCKERFILE=Dockerfile


build:
	@mkdir -p $(BINARY_DIR)
	go build $(LDFLAGS) -o $(BINARY_DIR)/$(BINARY_NAME) ./cmd/nipper

test:
	@mkdir -p $(COVER_DIR)
	go test ./... -v -race -coverprofile=$(COVER_DIR)/coverage.out -covermode=atomic

cover: test
	go tool cover -func=$(COVER_DIR)/coverage.out

cover-html: test
	go tool cover -html=$(COVER_DIR)/coverage.out -o $(COVER_DIR)/coverage.html
	open $(COVER_DIR)/coverage.html 2>/dev/null || xdg-open $(COVER_DIR)/coverage.html

clean:
	rm -rf $(BINARY_DIR) $(COVER_DIR)

# Build Docker image with CGO enabled using multi-stage build
docker-build:
	docker buildx build --load -t $(IMAGE_NAME):$(IMAGE_TAG) -f $(DOCKERFILE) .

# Build and tag Docker image for release
docker-release:
	docker buildx build --load -t $(IMAGE_NAME):$(IMAGE_TAG) -t $(IMAGE_NAME):latest -f $(DOCKERFILE) .

help:
	@echo "Available targets:"
	@echo "  build      - Build the nipper binary to $(BINARY_DIR)/"
	@echo "  test       - Run unit tests with coverage report"
	@echo "  cover      - Show test coverage summary"
	@echo "  cover-html - Generate HTML coverage report and open in browser"
	@echo "  clean      - Remove build and coverage directories"
	@echo "  docker-build  - Build Docker image (nipper:latest)"
	@echo "  docker-release - Build and tag Docker image for release (nipper:latest and nipper:TAG)"
