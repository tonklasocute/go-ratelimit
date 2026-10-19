# Modules in this repository. The root module is the core.
MODULES := . store/redis store/redis/integration \
	middleware/chi middleware/gin middleware/echo middleware/fiber middleware/grpc \
	examples
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0

.PHONY: all test race cover lint bench bench-redis integration visualize tidy compose

all: lint race cover

test: ## Run every module's tests
	@for m in $(MODULES); do echo "== $$m"; (cd $$m && go test ./...) || exit 1; done

race: ## Run every module's tests with the race detector
	@for m in $(MODULES); do echo "== $$m"; (cd $$m && go test -race ./...) || exit 1; done

cover: ## Core module coverage, across packages
	GOWORK=off go test -coverpkg=./... -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1

lint: ## Lint every module
	@for m in $(MODULES); do echo "== $$m"; (cd $$m && $(GOLANGCI_LINT) run ./...) || exit 1; done

bench: ## Algorithm and memory store benchmarks
	go test -run '^$$' -bench . -benchmem ./algorithm ./store/memory

bench-redis: ## Redis benchmarks (REDIS_ADDR or Docker)
	cd store/redis/integration && go test -run '^$$' -bench . -benchmem ./...

integration: ## Redis integration tests (REDIS_ADDR or Docker)
	cd store/redis/integration && go test -race -v ./...

visualize: ## Regenerate docs/img/*.svg
	go run ./cmd/visualize

tidy: ## go mod tidy in every module
	@for m in $(MODULES); do (cd $$m && go mod tidy) || exit 1; done

compose: ## Run the distributed example with k6, then print per-second counts
	docker compose -f examples/distributed/docker-compose.yml up --build --abort-on-container-exit --exit-code-from k6 k6
	docker compose -f examples/distributed/docker-compose.yml down
	examples/distributed/per-second.sh
