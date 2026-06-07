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
	totalRounds  = 1_000_000
	transferCap  = 1000
)

func TestSimulation(t *testing.T) {
	path := "sim-wal.jsonl"
	os.Remove(path)
	w, err := NewWallet(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	defer os.Remove(path)

	w.Create("exchange")
	w.Deposit("exchange", exchangeInit)

	active := make(map[string]bool)
	inactive := make(map[string]bool)

	users := make([]string, numUsers)
	for i := 0; i < numUsers; i++ {
		name := "u" + strconv.Itoa(i)
		users[i] = name
		w.Create(name)
		cap := rand.IntN(maxInitCap + 1)
		if cap > 0 {
			w.Transfer("exchange", name, cap)
		}
		active[name] = true
	}

	success := 0
	fail := 0
	var joinSeq []string

	rng := rand.New(rand.NewPCG(42, 0))

	for round := 0; round < totalRounds; round++ {
		if round < numUsers/2 && rng.IntN(100) == 0 {
			name := "u" + strconv.Itoa(numUsers+len(joinSeq))
			joinSeq = append(joinSeq, name)
			w.Create(name)
			cap := rng.IntN(maxInitCap + 1)
			if cap > 0 {
				w.Transfer("exchange", name, cap)
			}
			active[name] = true
		}

		if round > 0 && round%1000 == 0 {
			nLeave := 1 + rng.IntN(5)
			act := activeKeys(active)
			if len(act) > nLeave+10 {
				for _, name := range rng.Perm(len(act))[:nLeave] {
					delete(active, act[name])
					inactive[act[name]] = true
				}
			}
		}

		act := activeKeys(active)
		if len(act) < 2 {
			continue
		}
		idx := rng.Perm(len(act))
		from, to := act[idx[0]], act[idx[1]]

		fromBal, _ := w.Balance(from)
		if fromBal == 0 {
			fail++
			continue
		}
		amount := 1 + rng.IntN(min(fromBal, transferCap))
		if err := w.Transfer(from, to, amount); err != nil {
			fail++
		} else {
			success++
		}
	}

	exBal, _ := w.Balance("exchange")

	var actBals, inactBals []int
	for name := range active {
		bal, _ := w.Balance(name)
		actBals = append(actBals, bal)
	}
	for name := range inactive {
		bal, _ := w.Balance(name)
		inactBals = append(inactBals, bal)
	}
	sort.Ints(actBals)
	sort.Ints(inactBals)

	total := exBal
	for _, b := range actBals {
		total += b
	}
	for _, b := range inactBals {
		total += b
	}

	fmt.Println("=== 交易所模拟报告 ===")
	fmt.Printf("交易轮数: %d (成功 %d, 失败 %d)\n", totalRounds, success, fail)
	fmt.Printf("总用户: active=%d, inactive=%d, 中途入场=%d\n", len(active), len(inactive), len(joinSeq))
	fmt.Println()
	fmt.Printf("资金守恒: exchange_init=%d, current_sum=%d, diff=%d\n", exchangeInit, total, total-exchangeInit)
	fmt.Println()
	fmt.Printf("交易所余额: %d\n", exBal)
	fmt.Println()
	fmt.Println("活跃用户余额分布:")
	printDistrib(actBals)
	fmt.Println()
	fmt.Println("离场用户余额分布:")
	printDistrib(inactBals)

	if total != exchangeInit {
		t.Errorf("资金不守恒: %d != %d", total, exchangeInit)
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

func printDistrib(bals []int) {
	if len(bals) == 0 {
		fmt.Println("  (无数据)")
		return
	}
	p := func(frac float64) int {
		idx := int(frac * float64(len(bals)-1))
		return bals[idx]
	}
	sum := 0
	for _, b := range bals {
		sum += b
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

			bal, _ := w.Balance(a)
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
				bal, _ := w.Balance("bank")
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
			bal, _ := w.Balance(name)
			currentSum += bal
		}
		if currentSum != sum {
			t.Fatalf("op %d (%s): sum=%d, expected=%d", i, op, currentSum, sum)
		}

		for name := range exists {
			bal, _ := w.Balance(name)
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
				bal, _ := w.Balance(name)
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
				bal, _ := w.Balance(from)
				if bal >= amount {
					w.Transfer(from, to, amount)
				}
			}
		}

		// 最终验证
		currentSum := 0
		for name := range created {
			bal, _ := w.Balance(name)
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
