# Developer entry points. CI runs the same targets.
VERSION     ?= dev
IMAGE       ?= dockerbox:$(VERSION)
AGENT_IMAGE ?= dockerbox-agent:$(VERSION)

.PHONY: all ui build test lint vuln check images it dev-up dev-down clean

all: check build

ui: ## vendor xterm.js into the embedded static files
	cd web && npm ci --ignore-scripts --no-audit --no-fund && npm run build

build: ## build the binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION) -X main.agentImage=$(AGENT_IMAGE)" -o bin/dockerbox ./cmd/dockerbox

test: ## unit tests with race detector and coverage
	go test -race -covermode=atomic -coverprofile=coverage.out ./...

lint: ## formatting and static analysis
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "run gofmt -w ." && exit 1)
	go vet ./...
	go vet -tags integration ./test/integration/
	go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
	node --check internal/web/static/app.js
	node --check internal/web/static/i18n.js
	node --check internal/web/static/theme.js
	@# ZimaOS cannot parse variable interpolation; the published compose file must be literal.
	@! grep -nE '^[^#]*[$$][{]' docker-compose.yml || (echo "docker-compose.yml must not use variable interpolation (ZimaOS)" && exit 1)

vuln: ## known vulnerabilities in dependencies and the Go standard library
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

check: lint test

images: ## build both container images
	docker build -t $(IMAGE) --build-arg VERSION=$(VERSION) --build-arg AGENT_IMAGE=$(AGENT_IMAGE) .
	docker build -t $(AGENT_IMAGE) images/agent

it: ## integration tests against the local Docker engine
	IT_IMAGE=$(IMAGE) IT_AGENT_IMAGE=$(AGENT_IMAGE) ./test/integration/run.sh

dev-up: ## run the stack from locally built images on http://localhost:8420
	DOCKERBOX_VERSION=$(VERSION) docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d

dev-down:
	docker compose -f docker-compose.yml -f docker-compose.dev.yml down

clean:
	rm -rf bin coverage.out web/node_modules internal/web/static/vendor
