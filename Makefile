# 交易所模拟 - 基于 WAL 的钱包系统
#
# 用法: make [target]
# 默认 target: help

.DEFAULT_GOAL := help

.PHONY: help test test-sim test-pbt fuzz fuzz-crash bench cover run

help: ## 显示帮助
	@echo "targets:"
	@echo ""
	@awk 'BEGIN { FS=":.*## " } /^[a-zA-Z_-]+:.*## / { printf "  %-16s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

test: ## 运行所有测试
	go test -count=1 -timeout 120s ./...

test-sim: ## 交易所模拟（1万用户、100万轮交易），输出资金分布报告
	go test -v -run TestSimulation$$ -count=1 -timeout 30s .

test-pbt: ## 资金守恒 PBT（5000 次随机操作，每步校验不变式）
	go test -v -run TestSimPropertySum$$ -count=1 -timeout 30s .

fuzz: ## 模糊测试（随机命令序列，30 秒）
	go test -run='^$$' -fuzz=FuzzSimulation -fuzztime=30s -timeout 60s .

fuzz-crash: ## 崩溃恢复模糊测试
	go test -run='^$$' -fuzz=FuzzCrashRecovery -fuzztime=30s -timeout 60s .

bench: ## 基准测试
	go test -run='^$$' -bench=. -benchtime=3s -timeout 120s .

cover: ## 测试覆盖率报告
	go test -coverprofile=cover.out -timeout 30s ./...
	go tool cover -func=cover.out
	rm -f cover.out

run: ## 运行钱包演示
	go run .
