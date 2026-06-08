package main

import (
	"fmt"
	"strconv"
	"sync"
)

// WAL is an append-only write-ahead log.
type WAL interface {
	Append(t string, data map[string]string) (Value, error)
	Entries() []Value
	Sync() error
	Close() error
}

type Ledger struct {
	mu       sync.Mutex
	wal      WAL
	balances map[string]int
}

func NewLedger(path string) (*Ledger, error) {
	fw, err := NewFileWAL(path)
	if err != nil {
		return nil, err
	}
	return NewLedgerWithWAL(fw), nil
}

func NewLedgerWithWAL(wal WAL) *Ledger {
	l := &Ledger{
		wal:      wal,
		balances: make(map[string]int),
	}
	for _, v := range wal.Entries() {
		l.apply(v)
	}
	return l
}

// --- pure functions on Value ---

// valueInvolves returns true if the WAL entry references owner in any role.
func valueInvolves(v Value, owner string) bool {
	switch v.Type {
	case "wallet_create", "wallet_deposit", "wallet_withdraw":
		return v.Data["owner"] == owner
	case "wallet_transfer":
		return v.Data["from"] == owner || v.Data["to"] == owner
	default:
		return false
	}
}

// valueBalanceDelta returns the net balance change for owner caused by v.
// Returns an error if the amount field is not a valid integer (corrupt WAL).
func valueBalanceDelta(v Value, owner string) (int, error) {
	switch v.Type {
	case "wallet_deposit":
		if v.Data["owner"] != owner {
			return 0, nil
		}
		amt, err := strconv.Atoi(v.Data["amount"])
		if err != nil {
			return 0, fmt.Errorf("corrupt WAL entry %d: invalid amount %q", v.ID, v.Data["amount"])
		}
		return amt, nil
	case "wallet_withdraw":
		if v.Data["owner"] != owner {
			return 0, nil
		}
		amt, err := strconv.Atoi(v.Data["amount"])
		if err != nil {
			return 0, fmt.Errorf("corrupt WAL entry %d: invalid amount %q", v.ID, v.Data["amount"])
		}
		return -amt, nil
	case "wallet_transfer":
		amt, err := strconv.Atoi(v.Data["amount"])
		if err != nil {
			return 0, fmt.Errorf("corrupt WAL entry %d: invalid amount %q", v.ID, v.Data["amount"])
		}
		if v.Data["from"] == owner {
			return -amt, nil
		}
		if v.Data["to"] == owner {
			return amt, nil
		}
		return 0, nil
	default:
		return 0, nil
	}
}

// --- Ledger methods ---

func (l *Ledger) apply(v Value) {
	switch v.Type {
	case "wallet_create":
		l.balances[v.Data["owner"]] = 0
	case "wallet_deposit":
		amt, _ := strconv.Atoi(v.Data["amount"])
		l.balances[v.Data["owner"]] += amt
	case "wallet_withdraw":
		amt, _ := strconv.Atoi(v.Data["amount"])
		l.balances[v.Data["owner"]] -= amt
	case "wallet_transfer":
		amt, _ := strconv.Atoi(v.Data["amount"])
		l.balances[v.Data["from"]] -= amt
		l.balances[v.Data["to"]] += amt
	}
}

func (l *Ledger) Create(owner string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, exists := l.balances[owner]; exists {
		return fmt.Errorf("wallet %s already exists", owner)
	}

	v, err := l.wal.Append("wallet_create", map[string]string{"owner": owner})
	if err != nil {
		return err
	}
	l.apply(v)
	return nil
}

func (l *Ledger) Deposit(owner string, amount int) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, exists := l.balances[owner]; !exists {
		return fmt.Errorf("wallet %s not found", owner)
	}
	if amount <= 0 {
		return fmt.Errorf("deposit amount must be positive")
	}

	v, err := l.wal.Append("wallet_deposit", map[string]string{
		"owner":  owner,
		"amount": strconv.Itoa(amount),
	})
	if err != nil {
		return err
	}
	l.apply(v)
	return nil
}

func (l *Ledger) Withdraw(owner string, amount int) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	bal, exists := l.balances[owner]
	if !exists {
		return fmt.Errorf("wallet %s not found", owner)
	}
	if amount <= 0 {
		return fmt.Errorf("withdraw amount must be positive")
	}
	if bal < amount {
		return fmt.Errorf("insufficient balance: %s has %d, tried to withdraw %d", owner, bal, amount)
	}

	v, err := l.wal.Append("wallet_withdraw", map[string]string{
		"owner":  owner,
		"amount": strconv.Itoa(amount),
	})
	if err != nil {
		return err
	}
	l.apply(v)
	return nil
}

func (l *Ledger) Transfer(from, to string, amount int) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if from == to {
		return fmt.Errorf("cannot transfer to self")
	}
	fromBal, exists := l.balances[from]
	if !exists {
		return fmt.Errorf("wallet %s not found", from)
	}
	if _, exists := l.balances[to]; !exists {
		return fmt.Errorf("wallet %s not found", to)
	}
	if amount <= 0 {
		return fmt.Errorf("transfer amount must be positive")
	}
	if fromBal < amount {
		return fmt.Errorf("insufficient balance: %s has %d, tried to transfer %d", from, fromBal, amount)
	}

	v, err := l.wal.Append("wallet_transfer", map[string]string{
		"from":   from,
		"to":     to,
		"amount": strconv.Itoa(amount),
	})
	if err != nil {
		return err
	}
	l.apply(v)
	return nil
}

func (l *Ledger) Balance(owner string) (int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	bal, ok := l.balances[owner]
	return bal, ok
}

func (l *Ledger) Balances() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]int, len(l.balances))
	for k, v := range l.balances {
		out[k] = v
	}
	return out
}

func (l *Ledger) Entries() []Value {
	return l.wal.Entries()
}

func (l *Ledger) Close() error {
	return l.wal.Close()
}

// History returns all WAL entries involving owner, in WAL order.
func (l *Ledger) History(owner string) ([]Value, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, exists := l.balances[owner]; !exists {
		return nil, fmt.Errorf("wallet %s not found", owner)
	}

	var result []Value
	for _, v := range l.wal.Entries() {
		if valueInvolves(v, owner) {
			result = append(result, v)
		}
	}
	return result, nil
}

// Verify checks that owner's in-memory balance matches a WAL-derived computation.
// Returns nil if consistent, or an error describing the mismatch.
func (l *Ledger) Verify(owner string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, exists := l.balances[owner]; !exists {
		return fmt.Errorf("wallet %s not found", owner)
	}
	return l.verifyLocked(owner)
}

// Audit verifies all accounts against the WAL. Returns a map of owner→error
// for each account that failed verification. An empty map means all clean.
func (l *Ledger) Audit() map[string]error {
	l.mu.Lock()
	defer l.mu.Unlock()

	failures := make(map[string]error)
	for owner := range l.balances {
		if err := l.verifyLocked(owner); err != nil {
			failures[owner] = err
		}
	}
	return failures
}

// verifyLocked computes balance from WAL without acquiring mu.
func (l *Ledger) verifyLocked(owner string) error {
	derived := 0
	for _, v := range l.wal.Entries() {
		delta, err := valueBalanceDelta(v, owner)
		if err != nil {
			return err
		}
		derived += delta
	}

	actual := l.balances[owner]
	if derived != actual {
		return fmt.Errorf("%s: WAL-derived %d != in-memory %d", owner, derived, actual)
	}
	return nil
}
