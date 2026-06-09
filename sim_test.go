package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"testing"
)

const (
	exchangeInit = 100_000_000
	numUsers     = 10_000
	maxInitCap   = 10_000
	totalRounds  = 30_000 // 30–50 matches per round, ~1.2M total
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
	w, err := NewLedger(path)
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

		// User exit: swap-delete
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
		matches := 30 + rng.IntN(21) // 30~50 pairs
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

	// Collect final balances and gains/losses per user
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

	// Tally gains and losses
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

	fmt.Println("=== Exchange Simulation Report ===")
	fmt.Printf("Rounds: %d (success %d, fail %d, all-in %d)\n", totalRounds, success, fail, allIn)
	fmt.Printf("Users: active=%d, inactive=%d, mid-join=%d\n", len(activeSet), len(inactive), len(joinSeq))
	fmt.Println()
	fmt.Printf("Conservation: exchange_init=%d, current_sum=%d, diff=%d\n", exchangeInit, totalFinal, totalFinal-exchangeInit)
	fmt.Println()
	fmt.Printf("Exchange balance: %d\n", exBal)
	fmt.Println()
	fmt.Printf("Total initial user funds: %d\n", totalInitial)
	fmt.Printf("Total final user balances: %d (Δ: %+d, %.2f%%)\n",
		totalFinal-exBal, totalFinal-exBal-totalInitial,
		float64(totalFinal-exBal-totalInitial)/float64(totalInitial)*100)
	fmt.Println()
	fmt.Printf("Gain/loss distribution: up=%d, down=%d, even=%d\n", gainCount, lossCount, evenCount)
	fmt.Println()
	fmt.Println("Active user balance distribution:")
	printBalDistrib(results, func(r userResult) bool { return r.active })
	fmt.Println()
	fmt.Println("Exited user balance distribution:")
	printBalDistrib(results, func(r userResult) bool { return !r.active })
	fmt.Println()
	// All-in users (at least one transfer emptied their balance)
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
	fmt.Printf("All-in users: %d (emptied balance at least once)\n", len(allInSet))
	fmt.Printf("  Recovered to profit: %d\n", allInProfit)
	if len(allInProfitUsers) > 0 {
		sort.Slice(allInProfitUsers, func(i, j int) bool { return allInProfitUsers[i].final-allInProfitUsers[i].initial > allInProfitUsers[j].final-allInProfitUsers[j].initial })
		p := allInProfitUsers[len(allInProfitUsers)/2]
		fmt.Printf("    Median profit: +%d\n", p.final-p.initial)
	}
	fmt.Printf("  Down but not broke: %d\n", allInLoss)
	if len(allInLossUsers) > 0 {
		sort.Slice(allInLossUsers, func(i, j int) bool { return allInLossUsers[i].final-allInLossUsers[i].initial > allInLossUsers[j].final-allInLossUsers[j].initial })
		p := allInLossUsers[len(allInLossUsers)/2]
		fmt.Printf("    Median loss: %d\n", p.final-p.initial)
	}
	fmt.Printf("  Ended at zero: %d\n", allInBroke)
	if len(allInBrokeUsers) > 0 {
		var initials []int
		sumInit := 0
		for _, r := range allInBrokeUsers {
			initials = append(initials, r.initial)
			sumInit += r.initial
		}
		sort.Ints(initials)
		fmt.Printf("    Brought in: %d, avg: %d, min=%d p50=%d max=%d\n",
			sumInit, sumInit/len(allInBrokeUsers), initials[0], initials[len(initials)/2], initials[len(initials)-1])
	}

	if totalFinal != exchangeInit {
		t.Errorf("conservation broken: %d != %d", totalFinal, exchangeInit)
	}

	allBefore := w.Balances()
	w.Close()

	w2, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()

	allAfter := w2.Balances()
	for name, want := range allBefore {
		got, ok := allAfter[name]
		if !ok {
			t.Errorf("%s missing after recovery", name)
		}
		if got != want {
			t.Errorf("%s: before recovery %d, after %d", name, want, got)
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
		fmt.Println("  (no data)")
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

// --- PBT: conservation of funds ---

func TestSimPropertySum(t *testing.T) {
	path := "sim-pbt-wal.jsonl"
	os.Remove(path)
	defer os.Remove(path)

	w, err := NewLedger(path)
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

// --- Fuzz: random op sequences verify invariants ---

func FuzzSimulation(f *testing.F) {
	f.Add([]byte{0})

	f.Fuzz(func(t *testing.T, seed []byte) {
		f, err := os.CreateTemp("", "sim-fuzz-*.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		path := f.Name()
		f.Close()
		os.Remove(path)
		defer os.Remove(path)

		w, err := NewLedger(path)
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()

		w.Create("bank")
		w.Deposit("bank", 1_000_000_000)

		created := map[string]bool{"bank": true}
		sum := 1_000_000_000

		rng := seedRNG(seed)
		ops := 10 + rng.IntN(100)
		for op := 0; op < ops; op++ {
			owners := make([]string, 0, len(created))
			for o := range created {
				owners = append(owners, o)
			}

			switch rng.IntN(4) {
			case 0: // create
				name := "u" + strconv.Itoa(rng.IntN(100000))
				if !created[name] && w.Create(name) == nil {
					created[name] = true
				}

			case 1: // deposit
				if len(owners) == 0 {
					continue
				}
				name := owners[rng.IntN(len(owners))]
				amount := 1 + rng.IntN(100000)
				if w.Deposit(name, amount) == nil {
					sum += amount
				}

			case 2: // withdraw
				if len(owners) == 0 {
					continue
				}
				name := owners[rng.IntN(len(owners))]
				bal, ok := w.Balance(name)
				if !ok || bal == 0 {
					continue
				}
				amount := 1 + rng.IntN(min(bal, 10000))
				if w.Withdraw(name, amount) == nil {
					sum -= amount
				}

			case 3: // transfer
				if len(owners) < 2 {
					continue
				}
				i, j := rng.IntN(len(owners)), rng.IntN(len(owners))
				if i == j {
					continue
				}
				from, to := owners[i], owners[j]
				fromBal, ok := w.Balance(from)
				if !ok || fromBal == 0 {
					continue
				}
				amount := 1 + rng.IntN(min(fromBal, 10000))
				w.Transfer(from, to, amount)
			}
		}

		// Final verification
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
		w, err := NewLedger(path)
		assert(err == nil, "bench new ledger")
		assert(w.Create("exchange") == nil, "bench create exchange")
		assert(w.Deposit("exchange", exchangeInit) == nil, "bench fund exchange")
		for j := 0; j < 1000; j++ {
			name := "u" + strconv.Itoa(j)
			assert(w.Create(name) == nil, "bench create user")
			assert(w.Transfer("exchange", name, 100+rand.IntN(maxInitCap-100+1)) == nil, "bench fund user")
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
