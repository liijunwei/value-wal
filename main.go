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
	bw      *bufio.Writer
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
	fw.bw = bufio.NewWriter(f)
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
	if _, err := fw.bw.Write(b); err != nil {
		return Value{}, err
	}

	fw.next++
	fw.entries = append(fw.entries, v)
	return v, nil
}

func (fw *FileWAL) Sync() error {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if err := fw.bw.Flush(); err != nil {
		return err
	}
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
	path := "wallet.jsonl"
	os.Remove(path)

	w, err := NewWallet(path)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	defer w.Close()

	// 建钱包
	w.Create("alice")
	w.Create("bob")

	// 存款
	w.Deposit("alice", 1000)
	w.Deposit("bob", 500)

	// 转账
	if err := w.Transfer("alice", "bob", 300); err != nil {
		fmt.Println("transfer:", err)
	}

	// 余额不足
	if err := w.Withdraw("alice", 2000); err != nil {
		fmt.Println("withdraw rejected:", err)
	}

	// 正常提现
	w.Withdraw("alice", 200)

	fmt.Println("balances:")
	for owner, bal := range w.Balances() {
		fmt.Printf("  %s: %d\n", owner, bal)
	}

	// 崩溃恢复
	w.Close()
	w2, _ := NewWallet(path)
	defer w2.Close()
	fmt.Println("\nafter reopen:")
	for owner, bal := range w2.Balances() {
		fmt.Printf("  %s: %d\n", owner, bal)
	}
}
