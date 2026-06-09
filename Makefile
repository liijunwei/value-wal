# Exchange simulation - ledger system backed by WAL
#
# Usage: make [target]
# Default target: help

.DEFAULT_GOAL := help

.PHONY: help test test-sim test-pbt fuzz fuzz-crash fuzz-trace fuzz-all bench cover run

help: ## Show help
	@echo "targets:"
	@echo ""
	@awk 'BEGIN { FS=":.*## " } /^[a-zA-Z_-]+:.*## / { printf "  %-16s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

test: ## Run all tests
	go test -count=1 -timeout 120s ./...

test-sim: ## Exchange simulation (10k users, ~1.2M transfers), prints balance distribution report
	go test -v -run TestSimulation$$ -count=1 -timeout 30s .

test-pbt: ## Conservation PBT (5000 random ops, invariant check at each step)
	go test -v -run TestSimPropertySum$$ -count=1 -timeout 30s .

fuzz: ## Fuzz test (random op sequences, 30s)
	go test -run='^$$' -fuzz=FuzzLedger -fuzztime=30s -timeout 60s .

fuzz-crash: ## Fuzz test (crash recovery, 30s)
	go test -run='^$$' -fuzz=FuzzCrashRecovery -fuzztime=30s -timeout 60s .

fuzz-trace: ## Fuzz test (account traceability, 30s)
	go test -run='^$$' -fuzz=FuzzAccountTraceability -fuzztime=30s -timeout 60s .

fuzz-all: ## Fuzz test (all 3, 10s each)
	go test -run='^$$' -fuzz=FuzzLedger -fuzztime=10s -timeout 120s .
	go test -run='^$$' -fuzz=FuzzCrashRecovery -fuzztime=10s -timeout 120s .
	go test -run='^$$' -fuzz=FuzzAccountTraceability -fuzztime=10s -timeout 120s .

bench: ## Benchmark
	go test -run='^$$' -bench=. -benchtime=3s -timeout 120s .

cover: ## Test coverage report
	go test -coverprofile=cover.out -timeout 30s ./...
	go tool cover -func=cover.out
	rm -f cover.out

run: ## Run ledger demo
	go run .
