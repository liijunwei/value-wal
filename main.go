package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
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

type BankAccount struct {
	Owner   string `json:"owner"`
	Balance int    `json:"balance"`
}

func deriveState(entries []Value) []*BankAccount {
	accounts := make(map[string]*BankAccount)
	for _, v := range entries {
		switch v.Type {
		case "open":
			accounts[v.Data["owner"]] = &BankAccount{Owner: v.Data["owner"], Balance: 0}
		case "deposit":
			acc := accounts[v.Data["owner"]]
			amount, err := strconv.Atoi(v.Data["amount"])
			assert(err == nil, "parse deposit amount")
			acc.Balance += amount
		case "withdraw":
			acc := accounts[v.Data["owner"]]
			amount, err := strconv.Atoi(v.Data["amount"])
			assert(err == nil, "parse withdraw amount")
			acc.Balance -= amount
		case "transfer":
			from := accounts[v.Data["from"]]
			to := accounts[v.Data["to"]]
			amount, err := strconv.Atoi(v.Data["amount"])
			assert(err == nil, "parse transfer amount")
			from.Balance -= amount
			to.Balance += amount
		}
	}

	keys := make([]string, 0, len(accounts))
	for k := range accounts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]*BankAccount, len(keys))
	for i, k := range keys {
		result[i] = accounts[k]
	}
	return result
}

func assert(ok bool, msg string) {
	if !ok {
		panic("assertion failed: " + msg)
	}
}

func main() {
	path := "value-wal.jsonl"
	os.Remove(path)

	fw, err := NewFileWAL(path)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	fw.Append("open", map[string]string{"owner": "alice"})
	fw.Append("open", map[string]string{"owner": "bob"})
	fw.Append("deposit", map[string]string{"owner": "alice", "amount": "1000"})
	fw.Append("deposit", map[string]string{"owner": "bob", "amount": "500"})
	fw.Append("transfer", map[string]string{"from": "alice", "to": "bob", "amount": "300"})
	fw.Append("withdraw", map[string]string{"owner": "alice", "amount": "200"})
	fw.Append("deposit", map[string]string{"owner": "bob", "amount": "150"})

	entries := fw.Entries()
	fmt.Printf("WAL has %d entries in %s:\n", len(entries), path)
	for _, v := range entries {
		b, err := json.Marshal(v)
		assert(err == nil, "marshal Value")
		fmt.Printf("  %s\n", b)
	}

	fmt.Println("Current state (all 7 facts):")
	for _, acc := range deriveState(entries) {
		b, err := json.Marshal(acc)
		assert(err == nil, "marshal BankAccount")
		fmt.Printf("  %s\n", b)
	}

	fmt.Println("Historical state (first 5 facts only):")
	for _, acc := range deriveState(entries[:5]) {
		b, err := json.Marshal(acc)
		assert(err == nil, "marshal BankAccount")
		fmt.Printf("  %s\n", b)
	}

	fw.Close()

	fw2, _ := NewFileWAL(path)
	defer fw2.Close()
	fmt.Printf("\nReopened: %d entries recovered\n", len(fw2.Entries()))
}
