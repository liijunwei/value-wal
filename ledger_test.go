package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"math"
	"os"
	"strconv"
	"sync"
	"testing"
)

func tempPath(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "ledger-test-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	os.Remove(path)
	t.Cleanup(func() { os.Remove(path) })
	return path
}

func openLedger(t *testing.T) (*Ledger, string) {
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
	w, _ := openLedger(t)

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
	w, _ := openLedger(t)
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
	w, _ := openLedger(t)
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
	w, _ := openLedger(t)
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
	w, _ := openLedger(t)

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
	w, _ := openLedger(t)
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
	w, _ := openLedger(t)
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
		case "ledger_create":
			sumFromWAL[v.Data["owner"]] = 0
		case "ledger_deposit":
			amount, _ := strconv.Atoi(v.Data["amount"])
			sumFromWAL[v.Data["owner"]] += amount
		case "ledger_withdraw":
			amount, _ := strconv.Atoi(v.Data["amount"])
			sumFromWAL[v.Data["owner"]] -= amount
		case "ledger_transfer":
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
	w, _ := openLedger(t)
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
	w, _ := openLedger(t)
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
	w, _ := openLedger(t)
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
	w, _ := openLedger(t)
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
	w, _ := openLedger(t)
	w.Create("a")
	b1 := w.Balances()
	b1["x"] = 999 // mutate the copy
	_, ok := w.Balance("x")
	if ok {
		t.Fatal("Balances() should return a copy, not the internal map")
	}
}

// TestPropertyFuzzLedgerHarness validates the FuzzLedger state-tracking logic
// against the ledger's actual balances and independent WAL-derived computation.
// Uses fixed seeds so the sequence is deterministic and repeatable.
func TestPropertyFuzzLedgerHarness(t *testing.T) {
	seeds := [][]byte{
		{0},
		{1, 2, 3},
		{0xFF, 0x00, 0xAA, 0x55},
	}

	for s, seed := range seeds {
		w, _ := openLedger(t)
		rng := seedRNG(seed)

		created := map[string]bool{}
		expect := make(map[string]int)

		ops := 10 + rng.IntN(100)
		for op := 0; op < ops; op++ {
			owners := make([]string, 0, len(created))
			for o := range created {
				owners = append(owners, o)
			}

			switch rng.IntN(7) {
			case 0: // create
				name := "u" + strconv.Itoa(rng.IntN(100000))
				err := w.Create(name)
				if created[name] {
					if err == nil {
						t.Errorf("seed %d step %d: duplicate create %s should fail", s, op, name)
					}
				} else {
					if err != nil {
						t.Errorf("seed %d step %d: create %s: %v", s, op, name, err)
					}
					created[name] = true
					expect[name] = 0
				}

			case 1: // deposit (always valid)
				if len(owners) == 0 {
					continue
				}
				name := owners[rng.IntN(len(owners))]
				amount := 1 + rng.IntN(100000)
				if err := w.Deposit(name, amount); err != nil {
					t.Errorf("seed %d step %d: deposit %s %d: %v", s, op, name, amount, err)
				}
				expect[name] += amount

			case 2: // deposit to non-existent (must fail)
				name := "nx" + strconv.Itoa(rng.IntN(100000))
				if created[name] {
					continue
				}
				amount := 1 + rng.IntN(100000)
				if err := w.Deposit(name, amount); err == nil {
					t.Errorf("seed %d step %d: deposit to non-existent %s should fail", s, op, name)
				}

			case 3: // withdraw (may fail)
				if len(owners) == 0 {
					continue
				}
				name := owners[rng.IntN(len(owners))]
				bal := expect[name]
				amount := 1 + rng.IntN(bal + 100)
				err := w.Withdraw(name, amount)
				if bal < amount {
					if err == nil {
						t.Errorf("seed %d step %d: withdraw %s %d should fail (bal=%d)", s, op, name, amount, bal)
					}
				} else {
					if err != nil {
						t.Errorf("seed %d step %d: withdraw %s %d: %v", s, op, name, amount, err)
					}
					expect[name] -= amount
				}

			case 4: // withdraw from non-existent (must fail)
				name := "nx" + strconv.Itoa(rng.IntN(100000))
				if created[name] {
					continue
				}
				amount := 1 + rng.IntN(100000)
				if err := w.Withdraw(name, amount); err == nil {
					t.Errorf("seed %d step %d: withdraw from non-existent %s should fail", s, op, name)
				}

			case 5: // transfer (may fail)
				if len(owners) < 2 {
					continue
				}
				i, j := rng.IntN(len(owners)), rng.IntN(len(owners))
				if i == j {
					continue
				}
				from, to := owners[i], owners[j]
				fromBal := expect[from]
				amount := 1 + rng.IntN(fromBal + 100)
				err := w.Transfer(from, to, amount)
				if fromBal < amount {
					if err == nil {
						t.Errorf("seed %d step %d: transfer %s->%s %d should fail (bal=%d)", s, op, from, to, amount, fromBal)
					}
				} else {
					if err != nil {
						t.Errorf("seed %d step %d: transfer %s->%s %d: %v", s, op, from, to, amount, err)
					}
					expect[from] -= amount
					expect[to] += amount
				}

			case 6: // invalid transfer (must fail)
				if len(owners) == 0 {
					continue
				}
				from := owners[rng.IntN(len(owners))]
				amount := 1 + rng.IntN(100000)
				if rng.IntN(2) == 0 {
					if err := w.Transfer(from, from, amount); err == nil {
						t.Errorf("seed %d step %d: self-transfer %s should fail", s, op, from)
					}
				} else {
					to := "nx" + strconv.Itoa(rng.IntN(100000))
					if created[to] {
						continue
					}
					if err := w.Transfer(from, to, amount); err == nil {
						t.Errorf("seed %d step %d: transfer to non-existent %s->%s should fail", s, op, from, to)
					}
				}
			}

			// After each operation: expect must match actual ledger state
			for name, want := range expect {
				got, ok := w.Balance(name)
				if !ok {
					t.Fatalf("seed %d step %d: %s missing from ledger", s, op, name)
				}
				if got != want {
					t.Fatalf("seed %d step %d: %s expect=%d actual=%d", s, op, name, want, got)
				}
			}
		}

		// Final: expect must match independent WAL-derived computation
		for name := range expect {
			derived := 0
			for _, v := range w.Entries() {
				delta, err := valueBalanceDelta(v, name)
				if err != nil {
					t.Fatalf("seed %d: corrupt entry %d: %v", s, v.ID, err)
				}
				derived += delta
			}
			if derived != expect[name] {
				t.Errorf("seed %d: %s WAL-derived=%d expect=%d", s, name, derived, expect[name])
			}
		}
	}
}

// --- Fuzz helpers ---

// seedRNG derives a deterministic *rand.Rand from a fuzz seed byte slice.
// Different seeds produce different sequences; same seed always produces the same sequence.
func seedRNG(seed []byte) *rand.Rand {
	var a, b uint64
	for i, by := range seed {
		if i%2 == 0 {
			a = a ^ (a << 7) ^ uint64(by)
		} else {
			b = b ^ (b << 13) ^ uint64(by)
		}
	}
	return rand.New(rand.NewPCG(a, b))
}

// randRun executes random ledger operations driven by rng.
// Best-effort: operations may fail harmlessly (duplicate create, insufficient balance, etc.).
func randRun(rng *rand.Rand, w *Ledger, created map[string]bool) {
	ops := 10 + rng.IntN(100)
	for op := 0; op < ops; op++ {
		owners := make([]string, 0, len(created))
		for o := range created {
			owners = append(owners, o)
		}

		switch rng.IntN(4) {
		case 0: // create
			name := "u" + strconv.Itoa(rng.IntN(100000))
			if !created[name] {
				w.Create(name)
				created[name] = true
			}
		case 1: // deposit
			if len(owners) == 0 {
				continue
			}
			name := owners[rng.IntN(len(owners))]
			amount := 1 + rng.IntN(100000)
			w.Deposit(name, amount)
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
			w.Withdraw(name, amount)
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
}

// --- Fuzz tests ---

func FuzzLedger(f *testing.F) {
	f.Add([]byte{0})

	f.Fuzz(func(t *testing.T, seed []byte) {
		w, _ := openLedger(t)
		rng := seedRNG(seed)

		created := map[string]bool{}
		expect := make(map[string]int)

		ops := 10 + rng.IntN(100)
		for op := 0; op < ops; op++ {
			owners := make([]string, 0, len(created))
			for o := range created {
				owners = append(owners, o)
			}

			switch rng.IntN(7) {
			case 0: // create (random name, may be duplicate)
				name := "u" + strconv.Itoa(rng.IntN(100000))
				err := w.Create(name)
				if created[name] {
					if err == nil {
						t.Fatalf("step %d: duplicate create %s should fail", op, name)
					}
				} else {
					if err != nil {
						t.Fatalf("step %d: create %s: %v", op, name, err)
					}
					created[name] = true
					expect[name] = 0
				}

			case 1: // deposit (always valid)
				if len(owners) == 0 {
					continue
				}
				name := owners[rng.IntN(len(owners))]
				amount := 1 + rng.IntN(100000)
				if err := w.Deposit(name, amount); err != nil {
					t.Fatalf("step %d: deposit %s %d: %v", op, name, amount, err)
				}
				expect[name] += amount

			case 2: // deposit to non-existent (must fail)
				name := "nx" + strconv.Itoa(rng.IntN(100000))
				if created[name] {
					continue
				}
				amount := 1 + rng.IntN(100000)
				if err := w.Deposit(name, amount); err == nil {
					t.Fatalf("step %d: deposit to non-existent %s should fail", op, name)
				}

			case 3: // withdraw (may fail on insufficient balance)
				if len(owners) == 0 {
					continue
				}
				name := owners[rng.IntN(len(owners))]
				bal := expect[name]
				amount := 1 + rng.IntN(bal + 100) // may exceed balance
				err := w.Withdraw(name, amount)
				if bal < amount {
					if err == nil {
						t.Fatalf("step %d: withdraw %s %d should fail (bal=%d)", op, name, amount, bal)
					}
				} else {
					if err != nil {
						t.Fatalf("step %d: withdraw %s %d: %v", op, name, amount, err)
					}
					expect[name] -= amount
				}

			case 4: // withdraw from non-existent (must fail)
				name := "nx" + strconv.Itoa(rng.IntN(100000))
				if created[name] {
					continue
				}
				amount := 1 + rng.IntN(100000)
				if err := w.Withdraw(name, amount); err == nil {
					t.Fatalf("step %d: withdraw from non-existent %s should fail", op, name)
				}

			case 5: // transfer (may fail on insufficient balance)
				if len(owners) < 2 {
					continue
				}
				i, j := rng.IntN(len(owners)), rng.IntN(len(owners))
				if i == j {
					continue
				}
				from, to := owners[i], owners[j]
				fromBal := expect[from]
				amount := 1 + rng.IntN(fromBal + 100) // may exceed balance
				err := w.Transfer(from, to, amount)
				if fromBal < amount {
					if err == nil {
						t.Fatalf("step %d: transfer %s->%s %d should fail (bal=%d)", op, from, to, amount, fromBal)
					}
				} else {
					if err != nil {
						t.Fatalf("step %d: transfer %s->%s %d: %v", op, from, to, amount, err)
					}
					expect[from] -= amount
					expect[to] += amount
				}

			case 6: // invalid transfer: self or non-existent (must fail)
				if len(owners) == 0 {
					continue
				}
				from := owners[rng.IntN(len(owners))]
				amount := 1 + rng.IntN(100000)
				if rng.IntN(2) == 0 {
					// self-transfer
					if err := w.Transfer(from, from, amount); err == nil {
						t.Fatalf("step %d: self-transfer %s should fail", op, from)
					}
				} else {
					// transfer to non-existent
					to := "nx" + strconv.Itoa(rng.IntN(100000))
					if created[to] {
						continue
					}
					if err := w.Transfer(from, to, amount); err == nil {
						t.Fatalf("step %d: transfer to non-existent %s->%s should fail", op, from, to)
					}
				}
			}
		}

		// verify all balances match expected
		for owner, want := range expect {
			got, ok := w.Balance(owner)
			if !ok {
				t.Fatalf("%s missing from ledger", owner)
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
	f.Add([]byte{0})

	f.Fuzz(func(t *testing.T, seed []byte) {
		path := tempPath(t)
		rng := seedRNG(seed)
		expect := make(map[string]int)

		// first session: run random ops, track expected state
		func() {
			w, err := NewLedger(path)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()

			created := map[string]bool{}
			ops := 10 + rng.IntN(100)
			for op := 0; op < ops; op++ {
				owners := make([]string, 0, len(created))
				for o := range created {
					owners = append(owners, o)
				}

				switch rng.IntN(4) {
				case 0: // create
					name := "u" + strconv.Itoa(rng.IntN(100000))
					if !created[name] {
						if w.Create(name) == nil {
							created[name] = true
							expect[name] = 0
						}
					}
				case 1: // deposit
					if len(owners) == 0 {
						continue
					}
					name := owners[rng.IntN(len(owners))]
					amount := 1 + rng.IntN(100000)
					if w.Deposit(name, amount) == nil {
						expect[name] += amount
					}
				case 2: // withdraw
					if len(owners) == 0 {
						continue
					}
					name := owners[rng.IntN(len(owners))]
					bal := expect[name]
					if bal == 0 {
						continue
					}
					amount := 1 + rng.IntN(min(bal, 10000))
					if w.Withdraw(name, amount) == nil {
						expect[name] -= amount
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
					fromBal := expect[from]
					if fromBal == 0 {
						continue
					}
					amount := 1 + rng.IntN(min(fromBal, 10000))
					if w.Transfer(from, to, amount) == nil {
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
	w, _ := openLedger(t)
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
	w, _ := openLedger(t)
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

// --- Fuzz: transfer atomicity ---

func FuzzTransferAtomicity(f *testing.F) {
	f.Add([]byte{0})

	f.Fuzz(func(t *testing.T, seed []byte) {
		w, _ := openLedger(t)
		rng := seedRNG(seed)

		w.Create("bank")
		w.Deposit("bank", 1_000_000)

		created := map[string]bool{"bank": true}
		randRun(rng, w, created)

		for _, v := range w.Entries() {
			if v.Type != "ledger_transfer" {
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

// --- Fuzz: WAL append-only ---

func FuzzWALAppendOnly(f *testing.F) {
	f.Add([]byte{0})

	f.Fuzz(func(t *testing.T, seed []byte) {
		path := tempPath(t)
		rng := seedRNG(seed)

		w, err := NewLedger(path)
		if err != nil {
			t.Fatal(err)
		}

		w.Create("bank")
		w.Deposit("bank", 1_000_000)

		created := map[string]bool{"bank": true}
		randRun(rng, w, created)

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

// --- Fuzz: account traceability ---

func FuzzAccountTraceability(f *testing.F) {
	f.Add([]byte{0})

	f.Fuzz(func(t *testing.T, seed []byte) {
		w, _ := openLedger(t)
		rng := seedRNG(seed)

		w.Create("bank")
		w.Deposit("bank", 1_000_000)

		created := map[string]bool{"bank": true}
		randRun(rng, w, created)

		for name, actual := range w.Balances() {
			derived := 0
			for _, v := range w.Entries() {
				delta, err := valueBalanceDelta(v, name)
				if err != nil {
					t.Fatalf("corrupt entry %d: %v", v.ID, err)
				}
				derived += delta
			}
			if derived != actual {
				t.Errorf("%s: WAL-derived %d != ledger %d", name, derived, actual)
			}
		}
	})
}

// --- PBT: concurrent safety ---

func TestPropertyConcurrentSafety(t *testing.T) {
	w, _ := openLedger(t)

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

// --- Audit tests ---

func TestHistory(t *testing.T) {
	w, _ := openLedger(t)
	w.Create("alice")
	w.Create("bob")
	w.Deposit("alice", 1000)
	w.Transfer("alice", "bob", 300)
	w.Withdraw("bob", 50)

	entries, err := w.History("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 { // create, deposit, transfer
		t.Fatalf("alice: expected 3 entries, got %d", len(entries))
	}

	entries, err = w.History("bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 { // create, transfer, withdraw
		t.Fatalf("bob: expected 3 entries, got %d", len(entries))
	}

	_, err = w.History("nobody")
	if err == nil {
		t.Fatal("expected error for nonexistent owner")
	}
}

func TestVerify(t *testing.T) {
	w, _ := openLedger(t)
	w.Create("alice")
	w.Deposit("alice", 1000)
	w.Transfer("alice", "bob", 300) // auto-creates bob? no, bob must exist first

	// bob doesn't exist, so this should fail
	if err := w.Verify("bob"); err == nil {
		t.Fatal("expected error for nonexistent bob")
	}

	w.Create("bob")
	w.Transfer("alice", "bob", 300)

	if err := w.Verify("alice"); err != nil {
		t.Errorf("alice verification failed: %v", err)
	}
	if err := w.Verify("bob"); err != nil {
		t.Errorf("bob verification failed: %v", err)
	}
}

func TestAudit(t *testing.T) {
	w, _ := openLedger(t)

	w.Create("bank")
	w.Deposit("bank", 50000)

	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("u%d", i)
		w.Create(name)
		w.Transfer("bank", name, 100+i*10)
	}

	rng := rand.New(rand.NewPCG(42, 0))
	for i := 0; i < 100; i++ {
		from := fmt.Sprintf("u%d", rng.IntN(20))
		to := fmt.Sprintf("u%d", rng.IntN(20))
		if from == to {
			continue
		}
		bal, ok := w.Balance(from)
		if !ok || bal == 0 {
			continue
		}
		amt := 1 + rng.IntN(min(bal, 500))
		w.Transfer(from, to, amt)
	}

	failures := w.Audit()
	if len(failures) > 0 {
		for owner, err := range failures {
			t.Errorf("%s: %v", owner, err)
		}
	}
}

func TestAuditDetectsCorruption(t *testing.T) {
	w, _ := openLedger(t)
	w.Create("alice")
	w.Deposit("alice", 1000)

	failures := w.Audit()
	if len(failures) > 0 {
		t.Fatal("audit should be clean")
	}

	// corrupt in-memory state directly (bypass WAL)
	w.mu.Lock()
	w.balances["alice"] = 9999
	w.mu.Unlock()

	err := w.Verify("alice")
	if err == nil {
		t.Fatal("verify should detect the corruption")
	}
}

func TestHistoryOrder(t *testing.T) {
	w, _ := openLedger(t)
	w.Create("alice")
	w.Deposit("alice", 100)
	w.Deposit("alice", 200)
	w.Withdraw("alice", 50)

	entries, err := w.History("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}
	// entries must be in WAL order (creation order)
	if entries[0].Type != "ledger_create" {
		t.Error("first entry should be create")
	}
	if entries[1].Type != "ledger_deposit" || entries[1].Data["amount"] != "100" {
		t.Error("second entry should be deposit 100")
	}
	if entries[2].Type != "ledger_deposit" || entries[2].Data["amount"] != "200" {
		t.Error("third entry should be deposit 200")
	}
	if entries[3].Type != "ledger_withdraw" || entries[3].Data["amount"] != "50" {
		t.Error("fourth entry should be withdraw 50")
	}
}
