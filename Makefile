.PHONY: test test-sim test-pbt fuzz bench cover run lint

# 运行所有测试
test:
	go test -count=1 -timeout 120s ./...

# 运行交易所模拟（1万用户、100万轮交易），输出资金分布报告
test-sim:
	go test -v -run TestSimulation$$ -count=1 -timeout 30s .

# 运行资金守恒 PBT（5000 次随机操作，每步校验）
test-pbt:
	go test -v -run TestSimPropertySum$$ -count=1 -timeout 30s .

# 模糊测试（随机命令序列，30 秒）
fuzz:
	go test -fuzz=FuzzSimulation -fuzztime=30s -timeout 60s .

# 崩溃恢复模糊测试
fuzz-crash:
	go test -fuzz=FuzzCrashRecovery -fuzztime=30s -timeout 60s .

# 基准测试
bench:
	go test -bench=. -benchtime=3s -timeout 120s .

# 测试覆盖率报告
cover:
	go test -coverprofile=cover.out -timeout 30s ./...
	go tool cover -func=cover.out
	rm -f cover.out

# 运行钱包演示
run:
	go run .
