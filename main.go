package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

// A Value is an immutable fact. It never changes once written.
// This is the core idea from Hickey's "The Value of Values":
// values don't change — you perceive them, you don't "acquire" a mutable place.
type Value struct {
	ID   int
	Type string // "deposit" | "withdraw" | "transfer"
	Data map[string]string
}

// The WAL is an append-only log of immutable Values.
// You can only append; you can never modify or delete.
// This is the "simple" primitive — it does one thing.
type WAL struct {
	mu      sync.RWMutex
	entries []Value
}

func NewWAL(capacity int) *WAL {
	if capacity <= 0 {
		capacity = 64
	}
	return &WAL{entries: make([]Value, 0, capacity)}
}

// Append adds an immutable fact to the log. No mutation of existing data.
func (w *WAL) Append(t string, data map[string]string) Value {
	w.mu.Lock()
	defer w.mu.Unlock()

	v := Value{
		ID:   len(w.entries) + 1,
		Type: t,
		Data: data,
	}
	w.entries = append(w.entries, v)
	return v
}

// ReadFrom returns all values starting from a given offset.
// Each consumer controls its own offset — producer doesn't know or care
// where consumers are. Time is decoupled.
// Returns a copy so consumers don't pin the WAL's backing array.
func (w *WAL) ReadFrom(offset int) []Value {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if offset >= len(w.entries) {
		return nil
	}
	result := make([]Value, len(w.entries)-offset)
	copy(result, w.entries[offset:])
	return result
}

// Len returns total number of entries.
func (w *WAL) Len() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.entries)
}

// --- Consumer: reads WAL independently, maintains its own offset ---

type Consumer struct {
	Name   string
	wal    *WAL
	offset int // this consumer's place in the log — independent of others
}

func NewConsumer(name string, wal *WAL) *Consumer {
	return &Consumer{Name: name, wal: wal, offset: 0}
}

// Poll reads new values since last read. Each consumer moves at its own pace.
func (c *Consumer) Poll() []Value {
	values := c.wal.ReadFrom(c.offset)
	if len(values) > 0 {
		c.offset += len(values)
	}
	return values
}

// --- State is NOT mutated in place. It is DERIVED by replaying the WAL. ---

type BankAccount struct {
	Owner   string
	Balance int
}

// deriveState replays the entire WAL to produce current state.
// State is a function of the log: state = f(log).
// You don't ask "what's the balance right now?" (mutable place).
// You compute it from immutable facts.
func deriveState(wal *WAL) map[string]*BankAccount {
	wal.mu.RLock()
	defer wal.mu.RUnlock()

	accounts := make(map[string]*BankAccount)
	for _, v := range wal.entries {
		switch v.Type {
		case "open":
			accounts[v.Data["owner"]] = &BankAccount{Owner: v.Data["owner"], Balance: 0}
		case "deposit":
			acc := accounts[v.Data["owner"]]
			amount, _ := strconv.Atoi(v.Data["amount"])
			acc.Balance += amount
		case "withdraw":
			acc := accounts[v.Data["owner"]]
			amount, _ := strconv.Atoi(v.Data["amount"])
			acc.Balance -= amount
		case "transfer":
			from := accounts[v.Data["from"]]
			to := accounts[v.Data["to"]]
			amount, _ := strconv.Atoi(v.Data["amount"])
			from.Balance -= amount
			to.Balance += amount
		}
	}
	return accounts
}

// --- Persistent WAL on disk: append-only file ---

type FileWAL struct {
	mu      sync.Mutex
	file    *os.File
	next    int
	entries []Value // in-memory cache, same data as on disk
}

func NewFileWAL(path string) (*FileWAL, error) {
	fw := &FileWAL{next: 1}

	// Stream existing entries line by line — never loads the full file into memory.
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

// Sync flushes buffered writes to disk.
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
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var v Value
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			continue
		}
		entries = append(entries, v)
	}
	_ = scanner.Err() // partial read is acceptable; entries parsed so far are returned
	return entries
}

func main() {
	fmt.Print("=== Value-WAL: Hickey's 'Value of Values' in Practice ===\n\n")
	demo1_InMemory()
	demo2_DiskWAL()
}

func demo1_InMemory() {
	fmt.Println("--- Demo 1: In-memory WAL, independent consumers ---")
	wal := NewWAL(100)

	// Producer writes facts. These are immutable values — what "happened".
	// Nobody asks "what's the balance?" and gets a different answer each time.
	fmt.Println("Producer: appending facts to the WAL...")
	wal.Append("open", map[string]string{"owner": "alice"})
	wal.Append("open", map[string]string{"owner": "bob"})
	wal.Append("deposit", map[string]string{"owner": "alice", "amount": "1000"})
	wal.Append("deposit", map[string]string{"owner": "bob", "amount": "500"})
	wal.Append("transfer", map[string]string{"from": "alice", "to": "bob", "amount": "300"})
	wal.Append("withdraw", map[string]string{"owner": "alice", "amount": "200"})

	fmt.Printf("WAL has %d immutable entries\n\n", wal.Len())

	// Two consumers, each with their own independent offset.
	// They don't share mutable state. Each perceives the log at its own pace.
	// This is Hickey's "perception" vs "acquisition":
	// they perceive facts, never acquire a mutable reference.
	c1 := NewConsumer("audit-service", wal)
	c2 := NewConsumer("notification-service", wal)

	// Consumer 1 reads all at once.
	events := c1.Poll()
	fmt.Printf("  %s consumed %d events from offset 0\n", c1.Name, len(events))
	for _, v := range events {
		fmt.Printf("    [%d] %s %v\n", v.ID, v.Type, v.Data)
	}

	// Consumer 2 reads only first 2, then comes back later for the rest.
	// It controls its own pace. Producer doesn't know or care.
	slow := c2.Poll()[:2]
	fmt.Printf("\n  %s consumed first %d, paused at offset %d\n", c2.Name, len(slow), c2.offset)

	// Producer adds more facts. Consumer 2 was paused — it didn't miss anything.
	// The WAL is durable; facts are still there.
	fmt.Println("\nProducer: more facts...")
	wal.Append("deposit", map[string]string{"owner": "bob", "amount": "150"})

	// Consumer 2 resumes from where it left off.
	rest := c2.Poll()
	fmt.Printf("  %s resumed, caught up: %d more events\n", c2.Name, len(rest))
	for _, v := range rest {
		fmt.Printf("    [%d] %s %v\n", v.ID, v.Type, v.Data)
	}

	// Consumer 1 also sees the new fact.
	newEvents := c1.Poll()
	fmt.Printf("\n  %s got %d new event(s)\n", c1.Name, len(newEvents))

	// State is DERIVED from the WAL, not mutated in place.
	// Same log, same state. No "current version" of a mutable cell.
	fmt.Println("\n  state = f(log): deriving bank accounts from WAL replay...")
	accounts := deriveState(wal)
	for _, acc := range accounts {
		fmt.Printf("    %s: balance = %d\n", acc.Owner, acc.Balance)
	}

	// If we replay from only the first 5 entries, we get a different state.
	// This is point-in-time query — impossible with mutable state without snapshots.
	fmt.Println("\n  historical state (first 5 facts only):")
	partial := &WAL{entries: wal.entries[:5]}
	for _, acc := range deriveState(partial) {
		fmt.Printf("    %s: balance = %d\n", acc.Owner, acc.Balance)
	}

	fmt.Println()
}

func demo2_DiskWAL() {
	fmt.Println("--- Demo 2: Persistent file WAL (append-only, same as in-memory) ---")

	path := "/tmp/value-wal-demo.log"
	os.Remove(path)

	fw, err := NewFileWAL(path)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	// Same append-only pattern. Same immutable values.
	// The file is just bytes — no database, no broker, no server.
	fw.Append("open", map[string]string{"owner": "carol"})
	fw.Append("deposit", map[string]string{"owner": "carol", "amount": "5000"})
	fw.Append("withdraw", map[string]string{"owner": "carol", "amount": "1200"})

	fmt.Println("  Wrote 3 entries to", path)

	// Read back and derive state — same model.
	entries := fw.Entries()
	fmt.Printf("  File contains %d entries:\n", len(entries))
	for _, v := range entries {
		fmt.Printf("    [%d] %s %v\n", v.ID, v.Type, v.Data)
	}

	// Simulate crash recovery: reopen the file, it's still there.
	// The WAL survives because it's just an append-only file.
	fw.Close()
	time.Sleep(50 * time.Millisecond)

	fw2, _ := NewFileWAL(path)
	defer fw2.Close()
	fmt.Printf("\n  Reopened after 'crash': %d entries recovered\n", len(fw2.Entries()))

	fmt.Println()
	fmt.Println("Takeaway:")
	fmt.Println("  - WAL stores immutable facts (values), never mutates")
	fmt.Println("  - Consumers have independent offsets, decoupled from producer")
	fmt.Println("  - State is derived by replaying the log: state = f(log)")
	fmt.Println("  - Point-in-time is free: replay a prefix for historical state")
	fmt.Println("  - Persistence is just an append-only file — no broker needed")
}
