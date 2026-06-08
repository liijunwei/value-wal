package main

import (
	"fmt"
	"strconv"
	"sync"
)

type Ledger struct {
	mu       sync.Mutex
	wal      *FileWAL
	balances map[string]int
}

func NewLedger(path string) (*Ledger, error) {
	fw, err := NewFileWAL(path)
	if err != nil {
		return nil, err
	}

	w := &Ledger{
		wal:      fw,
		balances: make(map[string]int),
	}

	for _, v := range fw.Entries() {
		w.apply(v)
	}
	return w, nil
}

func (w *Ledger) apply(v Value) {
	switch v.Type {
	case "wallet_create":
		w.balances[v.Data["owner"]] = 0
	case "wallet_deposit":
		amount, _ := strconv.Atoi(v.Data["amount"])
		w.balances[v.Data["owner"]] += amount
	case "wallet_withdraw":
		amount, _ := strconv.Atoi(v.Data["amount"])
		w.balances[v.Data["owner"]] -= amount
	case "wallet_transfer":
		amount, _ := strconv.Atoi(v.Data["amount"])
		w.balances[v.Data["from"]] -= amount
		w.balances[v.Data["to"]] += amount
	}
}

func (w *Ledger) Create(owner string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, exists := w.balances[owner]; exists {
		return fmt.Errorf("wallet %s already exists", owner)
	}

	v, err := w.wal.Append("wallet_create", map[string]string{"owner": owner})
	if err != nil {
		return err
	}
	w.apply(v)
	return nil
}

func (w *Ledger) Deposit(owner string, amount int) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, exists := w.balances[owner]; !exists {
		return fmt.Errorf("wallet %s not found", owner)
	}
	if amount <= 0 {
		return fmt.Errorf("deposit amount must be positive")
	}

	v, err := w.wal.Append("wallet_deposit", map[string]string{
		"owner":  owner,
		"amount": strconv.Itoa(amount),
	})
	if err != nil {
		return err
	}
	w.apply(v)
	return nil
}

func (w *Ledger) Withdraw(owner string, amount int) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	bal, exists := w.balances[owner]
	if !exists {
		return fmt.Errorf("wallet %s not found", owner)
	}
	if amount <= 0 {
		return fmt.Errorf("withdraw amount must be positive")
	}
	if bal < amount {
		return fmt.Errorf("insufficient balance: %s has %d, tried to withdraw %d", owner, bal, amount)
	}

	v, err := w.wal.Append("wallet_withdraw", map[string]string{
		"owner":  owner,
		"amount": strconv.Itoa(amount),
	})
	if err != nil {
		return err
	}
	w.apply(v)
	return nil
}

func (w *Ledger) Transfer(from, to string, amount int) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if from == to {
		return fmt.Errorf("cannot transfer to self")
	}
	fromBal, exists := w.balances[from]
	if !exists {
		return fmt.Errorf("wallet %s not found", from)
	}
	if _, exists := w.balances[to]; !exists {
		return fmt.Errorf("wallet %s not found", to)
	}
	if amount <= 0 {
		return fmt.Errorf("transfer amount must be positive")
	}
	if fromBal < amount {
		return fmt.Errorf("insufficient balance: %s has %d, tried to transfer %d", from, fromBal, amount)
	}

	v, err := w.wal.Append("wallet_transfer", map[string]string{
		"from":   from,
		"to":     to,
		"amount": strconv.Itoa(amount),
	})
	if err != nil {
		return err
	}
	w.apply(v)
	return nil
}

func (w *Ledger) Balance(owner string) (int, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	bal, ok := w.balances[owner]
	return bal, ok
}

func (w *Ledger) Balances() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]int, len(w.balances))
	for k, v := range w.balances {
		out[k] = v
	}
	return out
}

func (w *Ledger) Close() error {
	return w.wal.Close()
}
