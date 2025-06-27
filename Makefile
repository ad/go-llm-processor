CWD = $(shell pwd)

APP_NAME := processor
VERSION := $(shell jq -r .version config.json)
OUTPUT := dist

PLATFORMS := \
    windows/amd64 \
    linux/amd64 \
    linux/arm64

SRC_DIRS := .
BUILD_VERSION=$(shell cat config.json | awk 'BEGIN { FS="\""; RS="," }; { if ($$2 == "version") {print $$4} }')
REPO=danielapatin/$(APP_NAME)

.PHONY: build publish

build:
	@BUILD_VERSION=$(BUILD_VERSION) KO_DOCKER_REPO=$(REPO) ko build ./cmd/processor --bare --local --sbom=none --tags="$(BUILD_VERSION),latest"

build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -a -installsuffix cgo ./cmd/processor

dev: dev-processor

dev-processor: ## Start only Go processor dev
	docker compose down processor-dev
	docker compose build processor-dev
	docker-compose up -d processor-dev
	docker-compose logs processor-dev -f

down:
	@docker compose down

publish:
	@BUILD_VERSION=$(BUILD_VERSION) KO_DOCKER_REPO=$(REPO) ko publish ./cmd/processor --bare --sbom=none --tags="$(BUILD_VERSION),latest"

lint:
	@golangci-lint run -v

test:
	@go test -v ./...

build-release:
	@for platform in $(PLATFORMS); do \
		GOOS=$${platform%/*} GOARCH=$${platform#*/} \
		output_name=$(APP_NAME)_$${platform%/*}_$${platform#*/}; \
		[ "$${platform%/*}" = "windows" ] && output_name=$$output_name.exe; \
		env GOOS=$${platform%/*} GOARCH=$${platform#*/} go build -ldflags="-X main.version=$(VERSION)" -o $(OUTPUT)/$$output_name ./cmd/processor; \
	done

release: build-release
	@cd $(OUTPUT) && \
	for file in *; do \
		case "$$file" in \
			*.exe) zip "$${file%.exe}.zip" "$$file" ;; \
			*) tar -czf "$$file.tar.gz" "$$file" ;; \
		esac; \
		rm -f "$$file"; \
	done
