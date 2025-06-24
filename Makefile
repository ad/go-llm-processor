CWD = $(shell pwd)
SRC_DIRS := .
BUILD_VERSION=$(shell cat config.json | awk 'BEGIN { FS="\""; RS="," }; { if ($$2 == "version") {print $$4} }')
REPO=danielapatin/go-llm-manager

.PHONY: build publish

build:
	@BUILD_VERSION=$(BUILD_VERSION) KO_DOCKER_REPO=$(REPO) ko build ./cmd/processor --bare --local --sbom=none --tags="$(BUILD_VERSION),latest"

dev: dev-processor

dev-processor: ## Start only Go processor dev
	docker compose build processor-dev
	docker-compose up -d processor-dev
	docker-compose logs processor-dev -f

publish:
	@BUILD_VERSION=$(BUILD_VERSION) KO_DOCKER_REPO=$(REPO) ko publish ./cmd/processor --bare --sbom=none --tags="$(BUILD_VERSION),latest"

lint:
	@golangci-lint run -v

test:
	@chmod +x ./test.sh
	@./test.sh $(SRC_DIRS)
