package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	exchangeInit = 100_000_000
	numUsers     = 10_000
	maxInitCap   = 10_000
	totalRounds  = 30_000 // 每轮 30~50 笔撮合 → 总量 ~1.2M 笔
	transferCap  = 1000
)

type userResult struct {
	name    string
	initial int
	final   int
	active  bool
}

func TestSimulation(t *testing.T) {
	path := "sim-wal.jsonl"
	os.Remove(path)
	w, err := NewWallet(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	defer os.Remove(path)

	assert(w.Create("exchange") == nil, "create exchange")
	assert(w.Deposit("exchange", exchangeInit) == nil, "fund exchange")

	activeList := make([]string, 0, numUsers)
	activeSet := make(map[string]bool, numUsers)
	inactive := make(map[string]bool)
	initialCap := make(map[string]int, numUsers)

	for i := 0; i < numUsers; i++ {
		name := "u" + strconv.Itoa(i)
		assert(w.Create(name) == nil, "create user")
		cap := 100 + rand.IntN(maxInitCap-100+1)
		assert(w.Transfer("exchange", name, cap) == nil, "initial fund transfer")
		activeList = append(activeList, name)
		activeSet[name] = true
		initialCap[name] = cap
	}

	success := 0
	fail := 0
	allIn := 0
	allInSet := make(map[string]bool)
	var joinSeq []string

	rng := rand.New(rand.NewPCG(42, 0))

	for round := 0; round < totalRounds; round++ {
		if rng.IntN(100) == 0 {
			name := "u" + strconv.Itoa(numUsers+len(joinSeq))
			joinSeq = append(joinSeq, name)
			assert(w.Create(name) == nil, "mid-join create")
			cap := 100 + rng.IntN(maxInitCap-100+1)
			assert(w.Transfer("exchange", name, cap) == nil, "mid-join fund")
			activeList = append(activeList, name)
			activeSet[name] = true
			initialCap[name] = cap
		}

		// 离场: swap-delete
		if round > 0 && round%200 == 0 {
			nLeave := 2 + rng.IntN(7)
			if len(activeList) > nLeave+10 {
				for k := 0; k < nLeave; k++ {
					i := rng.IntN(len(activeList))
					name := activeList[i]
					delete(activeSet, name)
					inactive[name] = true
					activeList[i] = activeList[len(activeList)-1]
					activeList = activeList[:len(activeList)-1]
				}
			}
		}

		if len(activeList) < 2 {
			continue
		}
		matches := 30 + rng.IntN(21) // 30~50 组
		for m := 0; m < matches; m++ {
			i, j := rng.IntN(len(activeList)), rng.IntN(len(activeList))
			from, to := activeList[i], activeList[j]

			fromBal, ok := w.Balance(from)
			assert(ok, "balance lookup")
			if fromBal == 0 {
				fail++
				continue
			}
			amount := 1 + rng.IntN(min(fromBal, transferCap))
			if amount == fromBal {
				allIn++
				allInSet[from] = true
			}
			if err := w.Transfer(from, to, amount); err != nil {
				fail++
			} else {
				success++
			}
		}
	}

	exBal, ok := w.Balance("exchange")
	assert(ok, "exchange balance")

	// 收集各用户最终余额与涨跌
	var results []userResult
	for name := range activeSet {
		bal, ok := w.Balance(name)
		assert(ok, "active balance lookup")
		results = append(results, userResult{name, initialCap[name], bal, true})
	}
	for name := range inactive {
		bal, ok := w.Balance(name)
		assert(ok, "inactive balance lookup")
		results = append(results, userResult{name, initialCap[name], bal, false})
	}

	// 统计涨跌
	gainCount, lossCount, evenCount := 0, 0, 0
	totalInitial := 0
	totalFinal := exBal
	for _, r := range results {
		totalInitial += r.initial
		totalFinal += r.final
		if r.final > r.initial {
			gainCount++
		} else if r.final < r.initial {
			lossCount++
		} else {
			evenCount++
		}
	}

	fmt.Println("=== 交易所模拟报告 ===")
	fmt.Printf("交易轮数: %d (成功 %d, 失败 %d, 梭哈 %d)\n", totalRounds, success, fail, allIn)
	fmt.Printf("总用户: active=%d, inactive=%d, 中途入场=%d\n", len(activeSet), len(inactive), len(joinSeq))
	fmt.Println()
	fmt.Printf("资金守恒: exchange_init=%d, current_sum=%d, diff=%d\n", exchangeInit, totalFinal, totalFinal-exchangeInit)
	fmt.Println()
	fmt.Printf("交易所余额: %d\n", exBal)
	fmt.Println()
	fmt.Printf("用户初始资金总计: %d\n", totalInitial)
	fmt.Printf("用户最终余额总计: %d (涨跌: %+d, %.2f%%)\n",
		totalFinal-exBal, totalFinal-exBal-totalInitial,
		float64(totalFinal-exBal-totalInitial)/float64(totalInitial)*100)
	fmt.Println()
	fmt.Printf("用户涨跌分布: 赚=%d, 亏=%d, 持平=%d\n", gainCount, lossCount, evenCount)
	fmt.Println()
	fmt.Println("活跃用户余额分布:")
	printBalDistrib(results, func(r userResult) bool { return r.active })
	fmt.Println()
	fmt.Println("离场用户余额分布:")
	printBalDistrib(results, func(r userResult) bool { return !r.active })
	fmt.Println()
	// 梭哈用户（至少一次转账清空余额）
	var allInProfit, allInLoss, allInBroke int
	var allInProfitUsers, allInLossUsers, allInBrokeUsers []userResult
	for _, r := range results {
		if allInSet[r.name] {
			if r.final == 0 {
				allInBroke++
				allInBrokeUsers = append(allInBrokeUsers, r)
			} else if r.final > r.initial {
				allInProfit++
				allInProfitUsers = append(allInProfitUsers, r)
			} else {
				allInLoss++
				allInLossUsers = append(allInLossUsers, r)
			}
		}
	}
	fmt.Printf("梭哈用户: %d 人（至少一次转账清空余额）\n", len(allInSet))
	fmt.Printf("  翻身盈利: %d 人\n", allInProfit)
	if len(allInProfitUsers) > 0 {
		sort.Slice(allInProfitUsers, func(i, j int) bool { return allInProfitUsers[i].final-allInProfitUsers[i].initial > allInProfitUsers[j].final-allInProfitUsers[j].initial })
		p := allInProfitUsers[len(allInProfitUsers)/2]
		fmt.Printf("    中位盈利: +%d\n", p.final-p.initial)
	}
	fmt.Printf("  亏损未归零: %d 人\n", allInLoss)
	if len(allInLossUsers) > 0 {
		sort.Slice(allInLossUsers, func(i, j int) bool { return allInLossUsers[i].final-allInLossUsers[i].initial > allInLossUsers[j].final-allInLossUsers[j].initial })
		p := allInLossUsers[len(allInLossUsers)/2]
		fmt.Printf("    中位亏损: %d\n", p.final-p.initial)
	}
	fmt.Printf("  最终归零: %d 人\n", allInBroke)
	if len(allInBrokeUsers) > 0 {
		var initials []int
		sumInit := 0
		for _, r := range allInBrokeUsers {
			initials = append(initials, r.initial)
			sumInit += r.initial
		}
		sort.Ints(initials)
		fmt.Printf("    带入资金: %d, 人均: %d, min=%d p50=%d max=%d\n",
			sumInit, sumInit/len(allInBrokeUsers), initials[0], initials[len(initials)/2], initials[len(initials)-1])
	}

	if totalFinal != exchangeInit {
		t.Errorf("资金不守恒: %d != %d", totalFinal, exchangeInit)
	}

	allBefore := w.Balances()
	w.Close()

	w2, err := NewWallet(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()

	allAfter := w2.Balances()
	for name, want := range allBefore {
		got, ok := allAfter[name]
		if !ok {
			t.Errorf("%s 在恢复后丢失", name)
		}
		if got != want {
			t.Errorf("%s: 恢复前 %d, 恢复后 %d", name, want, got)
		}
	}
}

func activeKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func printBalDistrib(results []userResult, filter func(userResult) bool) {
	var bals []int
	var sum int
	for _, r := range results {
		if filter(r) {
			bals = append(bals, r.final)
			sum += r.final
		}
	}
	if len(bals) == 0 {
		fmt.Println("  (无数据)")
		return
	}
	sort.Ints(bals)
	p := func(frac float64) int {
		idx := int(frac * float64(len(bals)-1))
		return bals[idx]
	}
	fmt.Printf("  count=%d  sum=%d  min=%d  p25=%d  p50=%d  p75=%d  p95=%d  p99=%d  max=%d\n",
		len(bals), sum, bals[0], p(0.25), p(0.50), p(0.75), p(0.95), p(0.99), bals[len(bals)-1])
}

// --- PBT: 资金守恒 ---

func TestSimPropertySum(t *testing.T) {
	path := "sim-pbt-wal.jsonl"
	os.Remove(path)
	defer os.Remove(path)

	w, err := NewWallet(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	w.Create("bank")
	w.Deposit("bank", 1_000_000)

	sum := 1_000_000
	exists := map[string]bool{"bank": true}

	rng := rand.New(rand.NewPCG(99, 0))
	ops := []string{"transfer", "deposit_to_bank", "new_user"}

	for i := 0; i < 5000; i++ {
		op := ops[rng.IntN(len(ops))]

		switch op {
		case "transfer":
			names := activeKeys(exists)
			if len(names) < 2 {
				continue
			}
			idx := rng.Perm(len(names))
			a, b := names[idx[0]], names[idx[1]]

			bal, ok := w.Balance(a)
			assert(ok, "balance a")
			if bal == 0 {
				continue
			}
			amount := 1 + rng.IntN(min(bal, 5000))
			w.Transfer(a, b, amount)

		case "deposit_to_bank":
			amount := 1 + rng.IntN(10000)
			if w.Deposit("bank", amount) == nil {
				sum += amount
			}

		case "new_user":
			name := "u" + strconv.Itoa(rng.IntN(1_000_000))
			if exists[name] {
				continue
			}
			if w.Create(name) == nil {
				exists[name] = true
				bal, ok := w.Balance("bank")
			assert(ok, "balance bank")
				if bal > 0 {
					amount := rng.IntN(min(bal, 10000) + 1)
					if amount > 0 {
						w.Transfer("bank", name, amount)
					}
				}
			}
		}

		currentSum := 0
		for name := range exists {
			bal, ok := w.Balance(name)
			assert(ok, "balance sum check")
			currentSum += bal
		}
		if currentSum != sum {
			t.Fatalf("op %d (%s): sum=%d, expected=%d", i, op, currentSum, sum)
		}

		for name := range exists {
			bal, ok := w.Balance(name)
			assert(ok, "balance negative check")
			if bal < 0 {
				t.Fatalf("op %d: %s balance=%d < 0", i, name, bal)
			}
		}
	}
}

// --- Fuzz: 随机操作序列验证不变式 ---

func FuzzSimulation(f *testing.F) {
	f.Add("c a c b d a 100 t a b 50")
	f.Add("c x c y c z t x y 10 d z 200 t z x 30 w y 5")

	f.Fuzz(func(t *testing.T, seed string) {
		path := "sim-fuzz-wal.jsonl"
		os.Remove(path)
		defer os.Remove(path)

		w, err := NewWallet(path)
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()

		w.Create("bank")
		w.Deposit("bank", 1_000_000_000)

		created := map[string]bool{"bank": true}
		sum := 1_000_000_000

		tokens := strings.Split(seed, " ")
		i := 0
		for i < len(tokens) {
			if tokens[i] == "" {
				i++
				continue
			}
			cmd := tokens[i]
			i++

			switch cmd {
			case "c":
				if i >= len(tokens) {
					break
				}
				name := tokens[i]
				i++
				if created[name] {
					continue
				}
				if w.Create(name) == nil {
					created[name] = true
				}

			case "d":
				if i+1 >= len(tokens) {
					break
				}
				name := tokens[i]
				amount, err := strconv.Atoi(tokens[i+1])
				i += 2
				if err != nil || amount <= 0 || !created[name] {
					continue
				}
				if w.Deposit(name, amount) == nil {
					sum += amount
				}

			case "w":
				if i+1 >= len(tokens) {
					break
				}
				name := tokens[i]
				amount, err := strconv.Atoi(tokens[i+1])
				i += 2
				if err != nil || amount <= 0 || !created[name] {
					continue
				}
				bal, ok := w.Balance(name)
				assert(ok, "fuzz withdraw balance")
				if bal >= amount && w.Withdraw(name, amount) == nil {
					sum -= amount
				}

			case "t":
				if i+2 >= len(tokens) {
					break
				}
				from, to := tokens[i], tokens[i+1]
				amount, err := strconv.Atoi(tokens[i+2])
				i += 3
				if err != nil || amount <= 0 || from == to || !created[from] || !created[to] {
					continue
				}
				bal, ok := w.Balance(from)
				assert(ok, "fuzz transfer balance")
				if bal >= amount {
					w.Transfer(from, to, amount)
				}
			}
		}

		// 最终验证
		currentSum := 0
		for name := range created {
			bal, ok := w.Balance(name)
			assert(ok, "fuzz final check")
			if bal < 0 {
				t.Fatalf("%s balance=%d < 0", name, bal)
			}
			currentSum += bal
		}
		if currentSum != sum {
			t.Fatalf("sum mismatch: current=%d, expected=%d", currentSum, sum)
		}
	})
}

func BenchmarkSimulation(b *testing.B) {
	for i := 0; i < b.N; i++ {
		path := "sim-bench-wal.jsonl"
		os.Remove(path)
		w, err := NewWallet(path)
		assert(err == nil, "bench new wallet")
		assert(w.Create("exchange") == nil, "bench create exchange")
		assert(w.Deposit("exchange", exchangeInit) == nil, "bench fund exchange")
		for j := 0; j < 1000; j++ {
			name := "u" + strconv.Itoa(j)
			assert(w.Create(name) == nil, "bench create user")
			assert(w.Transfer("exchange", name, rand.IntN(maxInitCap+1)) == nil, "bench fund user")
		}
		rng := rand.New(rand.NewPCG(42, 0))
		for round := 0; round < 100000; round++ {
			from := "u" + strconv.Itoa(rng.IntN(1000))
			to := "u" + strconv.Itoa(rng.IntN(1000))
			if from == to {
				continue
			}
			fromBal, ok := w.Balance(from)
			assert(ok, "bench balance")
			if fromBal > 0 {
				amount := 1 + rng.IntN(min(fromBal, 1000))
				w.Transfer(from, to, amount)
			}
		}
		w.Close()
		os.Remove(path)
	}
}
