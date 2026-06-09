package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

// Value is an immutable record written to the WAL.
type Value struct {
	ID   int               `json:"id"`
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}

// FileWAL is an append-only write-ahead log backed by an NDJSON file.
type FileWAL struct {
	mu      sync.Mutex
	file    *os.File
	next    int
	entries []Value
}

func NewFileWAL(path string) (*FileWAL, error) {
	fw := &FileWAL{next: 1}

	if rf, err := os.Open(path); err == nil {
		fw.entries = parseEntriesFrom(rf)
		rf.Close()
		fw.next = len(fw.entries) + 1
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	fw.file = f
	return fw, nil
}

func (fw *FileWAL) Append(t string, data map[string]string) (Value, error) {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	v := Value{ID: fw.next, Type: t, Data: data}
	b, err := json.Marshal(v)
	if err != nil {
		return Value{}, err
	}
	b = append(b, '\n')
	if _, err := fw.file.Write(b); err != nil {
		return Value{}, err
	}

	fw.next++
	fw.entries = append(fw.entries, v)
	return v, nil
}

func (fw *FileWAL) Sync() error {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	return fw.file.Sync()
}

func (fw *FileWAL) Close() error {
	fw.Sync()
	return fw.file.Close()
}

func (fw *FileWAL) Entries() []Value {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	return fw.entries
}

func parseEntriesFrom(r *os.File) []Value {
	var entries []Value
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var v Value
			assert(json.Unmarshal(line, &v) == nil, "corrupt WAL line, cannot recover")
			entries = append(entries, v)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			panic(err)
		}
	}
	return entries
}

func assert(ok bool, msg string) {
	if !ok {
		panic("assertion failed: " + msg)
	}
}

func main() {
	path := "ledger.jsonl"
	os.Remove(path)

	w, err := NewLedger(path)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	defer w.Close()

	// Create accounts
	w.Create("alice")
	w.Create("bob")

	// Deposit
	w.Deposit("alice", 1000)
	w.Deposit("bob", 500)

	// Transfer
	if err := w.Transfer("alice", "bob", 300); err != nil {
		fmt.Println("transfer:", err)
	}

	// Insufficient balance (rejected)
	if err := w.Withdraw("alice", 2000); err != nil {
		fmt.Println("withdraw rejected:", err)
	}

	// Withdraw
	if err := w.Withdraw("alice", 200); err != nil {
		fmt.Println("withdraw:", err)
	}

	fmt.Println("balances:")
	for owner, bal := range w.Balances() {
		fmt.Printf("  %s: %d\n", owner, bal)
	}

	// Audit
	fmt.Println("\naudit:")
	failures := w.Audit()
	if len(failures) == 0 {
		fmt.Println("  all accounts verified")
	}
	for owner, err := range failures {
		fmt.Printf("  %s: %v\n", owner, err)
	}

	fmt.Println("\nhistory:")
	for _, owner := range []string{"alice", "bob"} {
		entries, err := w.History(owner)
		if err != nil {
			fmt.Printf("  %s history error: %v\n", owner, err)
			continue
		}
		fmt.Printf("  %s (%d entries):\n", owner, len(entries))
		for _, v := range entries {
			switch v.Type {
			case "ledger_create":
				fmt.Printf("    create\n")
			case "ledger_deposit":
				fmt.Printf("    deposit +%s\n", v.Data["amount"])
			case "ledger_withdraw":
				fmt.Printf("    withdraw -%s\n", v.Data["amount"])
			case "ledger_transfer":
				if v.Data["from"] == owner {
					fmt.Printf("    transfer to %s -%s\n", v.Data["to"], v.Data["amount"])
				} else {
					fmt.Printf("    transfer from %s +%s\n", v.Data["from"], v.Data["amount"])
				}
			}
		}
	}

	// Crash recovery
	w.Close()
	w2, err := NewLedger(path)
	assert(err == nil, "reopen ledger after close")
	defer w2.Close()
	fmt.Println("\nafter reopen:")
	for owner, bal := range w2.Balances() {
		fmt.Printf("  %s: %d\n", owner, bal)
	}
}
