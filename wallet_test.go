package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func tempPath(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "wallet-test-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	os.Remove(path)
	t.Cleanup(func() { os.Remove(path) })
	return path
}

func openWallet(t *testing.T) (*Ledger, string) {
	t.Helper()
	path := tempPath(t)
	w, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w, path
}

// --- Unit tests ---

func TestCreate(t *testing.T) {
	w, _ := openWallet(t)

	if err := w.Create("alice"); err != nil {
		t.Fatal(err)
	}
	bal, ok := w.Balance("alice")
	if !ok {
		t.Fatal("alice not found")
	}
	if bal != 0 {
		t.Fatalf("expected 0, got %d", bal)
	}

	// duplicate
	if err := w.Create("alice"); err == nil {
		t.Fatal("expected duplicate create to fail")
	}
}

func TestDeposit(t *testing.T) {
	w, _ := openWallet(t)
	w.Create("alice")
	w.Create("bob")

	if err := w.Deposit("alice", 100); err != nil {
		t.Fatal(err)
	}
	bal, _ := w.Balance("alice")
	if bal != 100 {
		t.Fatalf("expected 100, got %d", bal)
	}

	// non-existent
	if err := w.Deposit("nobody", 10); err == nil {
		t.Fatal("expected deposit to non-existent wallet to fail")
	}
	// zero / negative
	if err := w.Deposit("alice", 0); err == nil {
		t.Fatal("expected deposit 0 to fail")
	}
	if err := w.Deposit("alice", -5); err == nil {
		t.Fatal("expected negative deposit to fail")
	}
	// bob untouched
	bal, _ = w.Balance("bob")
	if bal != 0 {
		t.Fatalf("bob should still be 0, got %d", bal)
	}
}

func TestWithdraw(t *testing.T) {
	w, _ := openWallet(t)
	w.Create("alice")
	w.Deposit("alice", 500)

	if err := w.Withdraw("alice", 200); err != nil {
		t.Fatal(err)
	}
	bal, _ := w.Balance("alice")
	if bal != 300 {
		t.Fatalf("expected 300, got %d", bal)
	}

	// insufficient
	if err := w.Withdraw("alice", 301); err == nil {
		t.Fatal("expected insufficient balance to fail")
	}
	// balance unchanged after failed withdraw
	bal, _ = w.Balance("alice")
	if bal != 300 {
		t.Fatalf("balance should be unchanged at 300, got %d", bal)
	}
	// non-existent
	if err := w.Withdraw("nobody", 10); err == nil {
		t.Fatal("expected withdraw from non-existent wallet to fail")
	}
	// zero / negative
	if err := w.Withdraw("alice", 0); err == nil {
		t.Fatal("expected withdraw 0 to fail")
	}
}

func TestTransfer(t *testing.T) {
	w, _ := openWallet(t)
	w.Create("alice")
	w.Create("bob")
	w.Deposit("alice", 500)

	if err := w.Transfer("alice", "bob", 300); err != nil {
		t.Fatal(err)
	}
	a, _ := w.Balance("alice")
	b, _ := w.Balance("bob")
	if a != 200 || b != 300 {
		t.Fatalf("expected alice=200, bob=300; got %d, %d", a, b)
	}

	// insufficient
	if err := w.Transfer("alice", "bob", 201); err == nil {
		t.Fatal("expected insufficient balance to fail")
	}
	// self
	if err := w.Transfer("alice", "alice", 10); err == nil {
		t.Fatal("expected self-transfer to fail")
	}
	// non-existent
	if err := w.Transfer("alice", "nobody", 10); err == nil {
		t.Fatal("expected transfer to non-existent to fail")
	}
	if err := w.Transfer("nobody", "alice", 10); err == nil {
		t.Fatal("expected transfer from non-existent to fail")
	}
	// zero / negative
	if err := w.Transfer("alice", "bob", 0); err == nil {
		t.Fatal("expected transfer 0 to fail")
	}

	// balances unchanged after failed ops
	a2, _ := w.Balance("alice")
	b2, _ := w.Balance("bob")
	if a2 != 200 || b2 != 300 {
		t.Fatalf("balances should be unchanged after failures: %d, %d", a2, b2)
	}
}

func TestCrashRecovery(t *testing.T) {
	path := tempPath(t)

	func() {
		w, err := NewLedger(path)
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()

		w.Create("alice")
		w.Create("bob")
		w.Deposit("alice", 1000)
		w.Deposit("bob", 500)
		w.Transfer("alice", "bob", 300)
		w.Withdraw("bob", 100)
	}()

	// reopen
	w2, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()

	expected := map[string]int{"alice": 700, "bob": 700}
	for owner, want := range expected {
		got, ok := w2.Balance(owner)
		if !ok {
			t.Fatalf("%s not found after recovery", owner)
		}
		if got != want {
			t.Fatalf("%s: expected %d, got %d", owner, want, got)
		}
	}
}

// --- Property-based tests ---

// Invariant: sum of all balances == net deposits (deposits + external_transfers_in - withdrawals - external_transfers_out)
func TestPropertyBalanceSum(t *testing.T) {
	w, _ := openWallet(t)

	owners := []string{"a", "b", "c", "d"}
	for _, o := range owners {
		w.Create(o)
	}

	net := 0
	ops := []struct {
		do   func() error
		delta int
	}{
		{func() error { return w.Deposit("a", 100) }, 100},
		{func() error { return w.Deposit("b", 200) }, 200},
		{func() error { return w.Deposit("c", 50) }, 50},
		{func() error { return w.Withdraw("a", 30) }, -30},
		{func() error { return w.Transfer("a", "d", 20) }, 0}, // transfer: net=0
		{func() error { return w.Transfer("b", "c", 80) }, 0},
		{func() error { return w.Withdraw("d", 5) }, -5},
		{func() error { return w.Deposit("d", 60) }, 60},
		{func() error { return w.Withdraw("c", 40) }, -40},
		{func() error { return w.Withdraw("b", 100) }, -100}, // b: 200-80-100=20
	}

	for _, op := range ops {
		if err := op.do(); err != nil {
			t.Fatal(err)
		}
		net += op.delta

		sum := 0
		for _, o := range owners {
			bal, _ := w.Balance(o)
			sum += bal
		}
		if sum != net {
			t.Fatalf("net=%d, sum=%d", net, sum)
		}
	}
}

// Invariant: no balance ever goes negative
func TestPropertyNoNegativeBalance(t *testing.T) {
	w, _ := openWallet(t)
	w.Create("x")
	w.Deposit("x", 1000)

	// try many withdraws with random amounts, failures should never cause negative balance
	for i := 0; i < 100; i++ {
		bal, _ := w.Balance("x")
		w.Withdraw("x", bal/2 + 1) // may fail, never corrupt
		if b, _ := w.Balance("x"); b < 0 {
			t.Fatalf("balance went negative: %d", b)
		}
	}
}

// Invariant: WAL only contains successful operations
func TestPropertyWALMatchesState(t *testing.T) {
	w, _ := openWallet(t)
	w.Create("a")
	w.Create("b")
	w.Deposit("a", 100)
	w.Withdraw("a", 200) // fails
	w.Deposit("b", 50)
	w.Transfer("a", "c", 10) // fails (c doesn't exist)
	w.Transfer("a", "b", 30) // ok

	balA, _ := w.Balance("a") // 100 - 30 = 70
	balB, _ := w.Balance("b") // 50 + 30 = 80

	if balA != 70 || balB != 80 {
		t.Fatalf("unexpected balances: a=%d, b=%d", balA, balB)
	}

	// re-derive from WAL entries, confirm match
	sumFromWAL := make(map[string]int)
	for _, v := range w.wal.Entries() {
		switch v.Type {
		case "wallet_create":
			sumFromWAL[v.Data["owner"]] = 0
		case "wallet_deposit":
			amount, _ := strconv.Atoi(v.Data["amount"])
			sumFromWAL[v.Data["owner"]] += amount
		case "wallet_withdraw":
			amount, _ := strconv.Atoi(v.Data["amount"])
			sumFromWAL[v.Data["owner"]] -= amount
		case "wallet_transfer":
			amount, _ := strconv.Atoi(v.Data["amount"])
			sumFromWAL[v.Data["from"]] -= amount
			sumFromWAL[v.Data["to"]] += amount
		}
	}
	for owner, want := range sumFromWAL {
		got, _ := w.Balance(owner)
		if got != want {
			t.Fatalf("%s: state=%d, WAL-derived=%d", owner, got, want)
		}
	}
}

// Invariant: Entries() returns data that exactly round-trips through JSON
func TestPropertyWALEntriesRoundTrip(t *testing.T) {
	w, _ := openWallet(t)
	w.Create("a")
	w.Deposit("a", 100)
	w.Withdraw("a", 30)

	for _, v := range w.wal.Entries() {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var v2 Value
		if err := json.Unmarshal(b, &v2); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if v.ID != v2.ID || v.Type != v2.Type {
			t.Fatalf("round-trip mismatch: %+v vs %+v", v, v2)
		}
		for k := range v.Data {
			if v.Data[k] != v2.Data[k] {
				t.Fatalf("round-trip data mismatch for key %s", k)
			}
		}
	}
}

// --- Edge case tests ---

func TestTransferAllBalance(t *testing.T) {
	w, _ := openWallet(t)
	w.Create("a")
	w.Create("b")
	w.Deposit("a", 500)

	if err := w.Transfer("a", "b", 500); err != nil {
		t.Fatal(err)
	}
	a, _ := w.Balance("a")
	b, _ := w.Balance("b")
	if a != 0 || b != 500 {
		t.Fatalf("expected a=0, b=500; got %d, %d", a, b)
	}
}

func TestManyAccounts(t *testing.T) {
	w, _ := openWallet(t)
	n := 100

	for i := 1; i <= n; i++ {
		name := "user_" + strconv.Itoa(i)
		if err := w.Create(name); err != nil {
			t.Fatal(err)
		}
		if err := w.Deposit(name, i*10); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= n; i++ {
		name := "user_" + strconv.Itoa(i)
		bal, ok := w.Balance(name)
		if !ok {
			t.Fatalf("%s not found", name)
		}
		if bal != i*10 {
			t.Fatalf("%s: expected %d, got %d", name, i*10, bal)
		}
	}
}

func TestLargeAmount(t *testing.T) {
	w, _ := openWallet(t)
	w.Create("a")
	w.Create("b")

	big := math.MaxInt64 / 2
	if err := w.Deposit("a", big); err != nil {
		t.Fatal(err)
	}
	if err := w.Transfer("a", "b", big); err != nil {
		t.Fatal(err)
	}
	a, _ := w.Balance("a")
	b, _ := w.Balance("b")
	if a != 0 || b != big {
		t.Fatalf("large transfer failed: a=%d, b=%d", a, b)
	}

	// overflow guard: deposit max/2 again should not panic
	if err := w.Deposit("b", big); err != nil {
		t.Fatal(err)
	}
}

func TestReopenEmptyWAL(t *testing.T) {
	path := tempPath(t)
	w, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Balances()) != 0 {
		t.Fatal("expected no accounts")
	}
	w.Close()
}

func TestBalancesReturnsCopy(t *testing.T) {
	w, _ := openWallet(t)
	w.Create("a")
	b1 := w.Balances()
	b1["x"] = 999 // mutate the copy
	_, ok := w.Balance("x")
	if ok {
		t.Fatal("Balances() should return a copy, not the internal map")
	}
}

// --- Fuzz test ---

func FuzzWallet(f *testing.F) {
	// seed corpus
	f.Add("c a c b d a 100 d b 50 t a b 20 w a 30")
	f.Add("c x d x 1000 w x 500 w x 500")
	f.Add("c one c two c three d one 10 t one two 5 t two three 3")

	f.Fuzz(func(t *testing.T, seed string) {
		w, _ := openWallet(t)

		// track expected via independent calculation
		expect := make(map[string]int)
		created := make(map[string]bool)
		opCount := 0

		lines := strings.Split(seed, " ")
		for _, line := range lines {
			if line == "" {
				continue
			}
			parts := strings.Split(line, " ")
			if len(parts) < 2 {
				continue
			}
			opCount++

			switch parts[0] {
			case "c": // create owner
				owner := parts[1]
				err := w.Create(owner)
				if created[owner] {
					if err == nil {
						t.Fatalf("step %d: duplicate create %s should fail", opCount, owner)
					}
				} else {
					if err != nil {
						t.Fatalf("step %d: create %s: %v", opCount, owner, err)
					}
					created[owner] = true
					expect[owner] = 0
				}

			case "d": // deposit owner amount
				if len(parts) < 3 {
					continue
				}
				owner := parts[1]
				amount, err := strconv.Atoi(parts[2])
				if err != nil {
					continue
				}
				werr := w.Deposit(owner, amount)
				if !created[owner] || amount <= 0 {
					if werr == nil {
						t.Fatalf("step %d: deposit %s %d should fail", opCount, owner, amount)
					}
				} else {
					if werr != nil {
						t.Fatalf("step %d: deposit %s %d: %v", opCount, owner, amount, werr)
					}
					expect[owner] += amount
				}

			case "w": // withdraw owner amount
				if len(parts) < 3 {
					continue
				}
				owner := parts[1]
				amount, err := strconv.Atoi(parts[2])
				if err != nil {
					continue
				}
				werr := w.Withdraw(owner, amount)
				if !created[owner] || amount <= 0 || expect[owner] < amount {
					if werr == nil {
						t.Fatalf("step %d: withdraw %s %d should fail (bal=%d)", opCount, owner, amount, expect[owner])
					}
				} else {
					if werr != nil {
						t.Fatalf("step %d: withdraw %s %d: %v", opCount, owner, amount, werr)
					}
					expect[owner] -= amount
				}

			case "t": // transfer from to amount
				if len(parts) < 4 {
					continue
				}
				from, to := parts[1], parts[2]
				amount, err := strconv.Atoi(parts[3])
				if err != nil {
					continue
				}
				terr := w.Transfer(from, to, amount)
				shouldFail := !created[from] || !created[to] || from == to || amount <= 0 || expect[from] < amount
				if shouldFail {
					if terr == nil {
						t.Fatalf("step %d: transfer %s->%s %d should fail", opCount, from, to, amount)
					}
				} else {
					if terr != nil {
						t.Fatalf("step %d: transfer %s->%s %d: %v", opCount, from, to, amount, terr)
					}
					expect[from] -= amount
					expect[to] += amount
				}
			}
		}

		// verify all balances match expected
		for owner, want := range expect {
			got, ok := w.Balance(owner)
			if !ok {
				t.Fatalf("%s missing from wallet", owner)
			}
			if got != want {
				t.Fatalf("%s: expected %d, got %d", owner, want, got)
			}
		}

		// verify no negative balances
		for owner, bal := range w.Balances() {
			if bal < 0 {
				t.Fatalf("%s has negative balance: %d", owner, bal)
			}
		}

		})
}

func FuzzCrashRecovery(f *testing.F) {
	f.Add("c a c b d a 100 d b 50 t a b 20 w a 10")

	f.Fuzz(func(t *testing.T, seed string) {
		path := tempPath(t)
		expect := make(map[string]int)

		// first session
		func() {
			w, err := NewLedger(path)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()

			created := make(map[string]bool)
			for _, line := range strings.Split(seed, " ") {
				parts := strings.Split(line, " ")
				if len(parts) < 2 {
					continue
				}
				switch parts[0] {
				case "c":
					owner := parts[1]
					if !created[owner] {
						w.Create(owner)
						created[owner] = true
						expect[owner] = 0
					}
				case "d":
					if len(parts) < 3 {
						continue
					}
					owner := parts[1]
					amount, _ := strconv.Atoi(parts[2])
					if created[owner] && amount > 0 && w.Deposit(owner, amount) == nil {
						expect[owner] += amount
					}
				case "w":
					if len(parts) < 3 {
						continue
					}
					owner := parts[1]
					amount, _ := strconv.Atoi(parts[2])
					if created[owner] && amount > 0 && expect[owner] >= amount && w.Withdraw(owner, amount) == nil {
						expect[owner] -= amount
					}
				case "t":
					if len(parts) < 4 {
						continue
					}
					from, to := parts[1], parts[2]
					amount, _ := strconv.Atoi(parts[3])
					if created[from] && created[to] && from != to && amount > 0 && expect[from] >= amount && w.Transfer(from, to, amount) == nil {
						expect[from] -= amount
						expect[to] += amount
					}
				}
			}
		}()

		// reopen: state must match
		w2, err := NewLedger(path)
		if err != nil {
			t.Fatal(err)
		}
		defer w2.Close()

		for owner, want := range expect {
			got, ok := w2.Balance(owner)
			if !ok {
				t.Fatalf("%s missing after recovery", owner)
			}
			if got != want {
				t.Fatalf("%s: expected %d, got %d after recovery", owner, want, got)
			}
		}
	})
}

func TestConcurrentDeposits(t *testing.T) {
	w, _ := openWallet(t)
	w.Create("shared")

	n := 100
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			w.Deposit("shared", 1)
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}

	bal, _ := w.Balance("shared")
	if bal != n {
		t.Fatalf("expected %d, got %d after %d concurrent deposits", n, bal, n)
	}
}

func TestConcurrentMixedOps(t *testing.T) {
	w, _ := openWallet(t)
	w.Create("a")
	w.Create("b")
	w.Deposit("a", 10000)
	w.Deposit("b", 10000)

	n := 200
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			switch i % 3 {
			case 0:
				errs <- w.Deposit("a", 1)
			case 1:
				errs <- w.Withdraw("b", 1)
			case 2:
				errs <- w.Transfer("b", "a", 1)
			}
		}(i)
	}

	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	}

	a, _ := w.Balance("a")
	b, _ := w.Balance("b")
	// transfers: b→a, deposits: a, withdraws: b
	// sum remains 20000
	if a+b != 20000 {
		t.Fatalf("sum invariant broken: a=%d, b=%d, sum=%d", a, b, a+b)
	}
}

// --- Fuzz: transfer 原子性 ---

func FuzzTransferAtomicity(f *testing.F) {
	f.Add("c a c b d a 100 t a b 50")
	f.Add("c x c y c z d x 500 t x y 20 t z x 30 w y 5")

	f.Fuzz(func(t *testing.T, seed string) {
		w, _ := openWallet(t)
		w.Create("bank")
		w.Deposit("bank", 1_000_000)

		created := map[string]bool{"bank": true}
		parseAndRun(t, seed, w, created)

		for _, v := range w.Entries() {
			if v.Type != "wallet_transfer" {
				continue
			}
			from := v.Data["from"]
			to := v.Data["to"]
			amount, err := strconv.Atoi(v.Data["amount"])
			if err != nil {
				t.Errorf("transfer entry %d: invalid amount", v.ID)
			}
			if from == "" {
				t.Errorf("transfer entry %d: missing from", v.ID)
			}
			if to == "" {
				t.Errorf("transfer entry %d: missing to", v.ID)
			}
			if from == to {
				t.Errorf("transfer entry %d: self-transfer", v.ID)
			}
			if amount <= 0 {
				t.Errorf("transfer entry %d: non-positive amount %d", v.ID, amount)
			}
		}
	})
}

// --- Fuzz: WAL 只追加 ---

func FuzzWALAppendOnly(f *testing.F) {
	f.Add("c a c b d a 100 d b 50 t a b 20")

	f.Fuzz(func(t *testing.T, seed string) {
		path := tempPath(t)

		w, err := NewLedger(path)
		if err != nil {
			t.Fatal(err)
		}

		w.Create("bank")
		w.Deposit("bank", 1_000_000)

		created := map[string]bool{"bank": true}
		parseAndRun(t, seed, w, created)

		entriesBefore := w.Entries()
		countBefore := len(entriesBefore)

		w.Close()
		w2, err := NewLedger(path)
		if err != nil {
			t.Fatal(err)
		}
		defer w2.Close()

		entriesAfter := w2.Entries()
		if len(entriesAfter) != countBefore {
			t.Fatalf("entry count changed after reopen: %d -> %d", countBefore, len(entriesAfter))
		}
		for i, v := range entriesBefore {
			v2 := entriesAfter[i]
			if v.ID != v2.ID || v.Type != v2.Type {
				t.Errorf("WAL entry %d changed after reopen", i)
			}
		}
	})
}

// --- Fuzz: 单账户可溯源 ---

func FuzzAccountTraceability(f *testing.F) {
	f.Add("c a c b d a 100 d b 50 t a b 20 w a 30")
	f.Add("c x c y c z d x 500 t x y 100 t z x 200 w z 50")

	f.Fuzz(func(t *testing.T, seed string) {
		w, _ := openWallet(t)
		w.Create("bank")
		w.Deposit("bank", 1_000_000)

		created := map[string]bool{"bank": true}
		parseAndRun(t, seed, w, created)

		for name, actual := range w.Balances() {
			derived := 0
			for _, v := range w.Entries() {
				switch v.Type {
				case "wallet_deposit":
					if v.Data["owner"] == name {
						amt, _ := strconv.Atoi(v.Data["amount"])
						derived += amt
					}
				case "wallet_withdraw":
					if v.Data["owner"] == name {
						amt, _ := strconv.Atoi(v.Data["amount"])
						derived -= amt
					}
				case "wallet_transfer":
					if v.Data["from"] == name {
						amt, _ := strconv.Atoi(v.Data["amount"])
						derived -= amt
					}
					if v.Data["to"] == name {
						amt, _ := strconv.Atoi(v.Data["amount"])
						derived += amt
					}
				}
			}
			if derived != actual {
				t.Errorf("%s: WAL-derived %d != ledger %d", name, derived, actual)
			}
		}
	})
}

// --- PBT: 并发安全 ---

func TestPropertyConcurrentSafety(t *testing.T) {
	w, _ := openWallet(t)

	w.Create("bank")
	w.Deposit("bank", 1_000_000)

	n := 50
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			name := fmt.Sprintf("u%d", id)
			w.Create(name)
			w.Transfer("bank", name, 100+rand.IntN(5000))
		}(i)
	}
	wg.Wait()

	rng := rand.New(rand.NewPCG(99, 0))
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			from := fmt.Sprintf("u%d", rng.IntN(n))
			to := fmt.Sprintf("u%d", rng.IntN(n))
			if from == to {
				return
			}
			bal, ok := w.Balance(from)
			if !ok || bal == 0 {
				return
			}
			amt := 1 + rng.IntN(min(bal, 1000))
			w.Transfer(from, to, amt)
		}()
	}
	wg.Wait()

	sum := 0
	for _, bal := range w.Balances() {
		if bal < 0 {
			t.Errorf("negative balance after concurrent ops: %d", bal)
		}
		sum += bal
	}
	if sum != 1_000_000 {
		t.Errorf("conservation broken: sum=%d, expected=%d", sum, 1_000_000)
	}
}

func parseAndRun(t *testing.T, seed string, w *Ledger, created map[string]bool) {
	t.Helper()
	for _, line := range strings.Split(seed, " ") {
		parts := strings.Split(line, " ")
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case "c":
			owner := parts[1]
			if !created[owner] {
				w.Create(owner)
				created[owner] = true
			}
		case "d":
			if len(parts) < 3 {
				continue
			}
			owner := parts[1]
			amount, err := strconv.Atoi(parts[2])
			if err != nil || amount <= 0 || !created[owner] {
				continue
			}
			w.Deposit(owner, amount)
		case "w":
			if len(parts) < 3 {
				continue
			}
			owner := parts[1]
			amount, err := strconv.Atoi(parts[2])
			if err != nil || amount <= 0 || !created[owner] {
				continue
			}
			bal, ok := w.Balance(owner)
			if ok && bal >= amount {
				w.Withdraw(owner, amount)
			}
		case "t":
			if len(parts) < 4 {
				continue
			}
			from, to := parts[1], parts[2]
			amount, err := strconv.Atoi(parts[3])
			if err != nil || amount <= 0 || from == to || !created[from] || !created[to] {
				continue
			}
			bal, ok := w.Balance(from)
			if ok && bal >= amount {
				w.Transfer(from, to, amount)
			}
		}
	}
}