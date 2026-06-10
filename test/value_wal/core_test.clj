(ns value-wal.core-test
  (:require [clojure.test :refer :all]
            [clojure.data.json :as json]
            [clojure.java.io :as io]
            [value-wal.core :as sut])
  (:import [value_wal.core FileWAL Value Ledger]
           [java.util.concurrent Executors TimeUnit]
           [java.util.concurrent.atomic AtomicInteger]))

;; ═══════════════════════════════════════════════════════════════
;; Test helpers
;; ═══════════════════════════════════════════════════════════════

(defn- temp-path
  "Returns a unique temporary file path (creates and deletes the file)."
  []
  (let [f (java.io.File/createTempFile "ledger-test-" ".jsonl")]
    (.delete f)
    (str f)))

(defn- open-ledger
  "Creates a fresh Ledger on a temp file. Cleans up after test."
  [^Ledger l]
  (let [path (temp-path)]
    ;; Clean up WAL file on exit
    (.addShutdownHook (Runtime/getRuntime)
                      (Thread. #(try (io/delete-file path true)
                                    (catch Exception _))))
    path))

(defn- with-ledger
  "Fixture-style helper: calls (f ledger). Closes ledger after."
  [f]
  (let [path (temp-path)
        l    (sut/ledger path)]
    (try
      (f l)
      (finally
        (try (sut/ledger-close! l) (catch Exception _))
        (try (io/delete-file path true) (catch Exception _))))))

(defn- with-ledger-path
  "Like with-ledger but passes [ledger path] to f."
  [f]
  (let [path (temp-path)
        l    (sut/ledger path)]
    (try
      (f l path)
      (finally
        (try (sut/ledger-close! l) (catch Exception _))
        (try (io/delete-file path true) (catch Exception _))))))

;; ═══════════════════════════════════════════════════════════════
;; Unit tests — basic operations
;; ═══════════════════════════════════════════════════════════════

(deftest test-create
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "alice")
      (let [[bal ok?] (sut/ledger-balance l "alice")]
        (is ok?)
        (is (= 0 bal)))
      ;; duplicate
      (is (thrown? Exception (sut/ledger-create! l "alice"))))))

(deftest test-deposit
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "alice")
      (sut/ledger-create! l "bob")
      (sut/ledger-deposit! l "alice" 100)
      (is (= 100 (first (sut/ledger-balance l "alice"))))
      ;; non-existent
      (is (thrown? Exception (sut/ledger-deposit! l "nobody" 10)))
      ;; zero / negative
      (is (thrown? Exception (sut/ledger-deposit! l "alice" 0)))
      (is (thrown? Exception (sut/ledger-deposit! l "alice" -5)))
      ;; bob untouched
      (is (= 0 (first (sut/ledger-balance l "bob")))))))

(deftest test-withdraw
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "alice")
      (sut/ledger-deposit! l "alice" 500)
      (sut/ledger-withdraw! l "alice" 200)
      (is (= 300 (first (sut/ledger-balance l "alice"))))
      ;; insufficient
      (is (thrown? Exception (sut/ledger-withdraw! l "alice" 301)))
      ;; balance unchanged after failed withdraw
      (is (= 300 (first (sut/ledger-balance l "alice"))))
      ;; non-existent
      (is (thrown? Exception (sut/ledger-withdraw! l "nobody" 10)))
      ;; zero / negative
      (is (thrown? Exception (sut/ledger-withdraw! l "alice" 0))))))

(deftest test-transfer
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "alice")
      (sut/ledger-create! l "bob")
      (sut/ledger-deposit! l "alice" 500)
      (sut/ledger-transfer! l "alice" "bob" 300)
      (is (= 200 (first (sut/ledger-balance l "alice"))))
      (is (= 300 (first (sut/ledger-balance l "bob"))))
      ;; insufficient
      (is (thrown? Exception (sut/ledger-transfer! l "alice" "bob" 201)))
      ;; self
      (is (thrown? Exception (sut/ledger-transfer! l "alice" "alice" 10)))
      ;; non-existent
      (is (thrown? Exception (sut/ledger-transfer! l "alice" "nobody" 10)))
      (is (thrown? Exception (sut/ledger-transfer! l "nobody" "alice" 10)))
      ;; zero / negative
      (is (thrown? Exception (sut/ledger-transfer! l "alice" "bob" 0)))
      ;; balances unchanged after failed ops
      (is (= 200 (first (sut/ledger-balance l "alice"))))
      (is (= 300 (first (sut/ledger-balance l "bob")))))))

(deftest test-crash-recovery
  (let [path (temp-path)]
    ;; First session
    (let [l (sut/ledger path)]
      (sut/ledger-create! l "alice")
      (sut/ledger-create! l "bob")
      (sut/ledger-deposit! l "alice" 1000)
      (sut/ledger-deposit! l "bob" 500)
      (sut/ledger-transfer! l "alice" "bob" 300)
      (sut/ledger-withdraw! l "bob" 100)
      (sut/ledger-close! l))
    ;; Reopen
    (let [l2 (sut/ledger path)]
      (try
        (is (= 700 (first (sut/ledger-balance l2 "alice"))))
        (is (= 700 (first (sut/ledger-balance l2 "bob"))))
        (finally
          (sut/ledger-close! l2)
          (io/delete-file path true))))))

;; ═══════════════════════════════════════════════════════════════
;; Property-based tests
;; ═══════════════════════════════════════════════════════════════

(deftest test-property-balance-sum
  ;; Invariant: sum of all balances == net deposits
  (with-ledger
    (fn [l]
      (doseq [o ["a" "b" "c" "d"]] (sut/ledger-create! l o))
      (let [ops [[#(sut/ledger-deposit! l "a" 100)  100]
                 [#(sut/ledger-deposit! l "b" 200)  200]
                 [#(sut/ledger-deposit! l "c" 50)   50]
                 [#(sut/ledger-withdraw! l "a" 30)  -30]
                 [#(sut/ledger-transfer! l "a" "d" 20)  0]
                 [#(sut/ledger-transfer! l "b" "c" 80)  0]
                 [#(sut/ledger-withdraw! l "d" 5)   -5]
                 [#(sut/ledger-deposit! l "d" 60)   60]
                 [#(sut/ledger-withdraw! l "c" 40)  -40]
                 [#(sut/ledger-withdraw! l "b" 100) -100]]]
        (loop [remaining ops, net 0]
          (when-let [[op delta] (first remaining)]
            (op)
            (let [net' (+ net delta)
                  sum  (reduce + (vals (sut/ledger-balances l)))]
              (is (= net' sum) (str "net=" net' ", sum=" sum))
              (recur (rest remaining) net'))))))))

(deftest test-property-no-negative-balance
  ;; Invariant: no balance ever goes negative
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "x")
      (sut/ledger-deposit! l "x" 1000)
      (dotimes [_ 100]
        (let [bal (first (sut/ledger-balance l "x"))]
          (try (sut/ledger-withdraw! l "x" (inc (quot bal 2)))
               (catch Exception _))
          (is (not (neg? (first (sut/ledger-balance l "x"))))
              "balance went negative"))))))

(deftest test-property-wal-matches-state
  ;; Invariant: WAL only contains successful operations
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "a")
      (sut/ledger-create! l "b")
      (sut/ledger-deposit! l "a" 100)
      (try (sut/ledger-withdraw! l "a" 200) (catch Exception _))  ;; fails
      (sut/ledger-deposit! l "b" 50)
      (try (sut/ledger-transfer! l "a" "c" 10) (catch Exception _)) ;; fails
      (sut/ledger-transfer! l "a" "b" 30)
      (is (= 70 (first (sut/ledger-balance l "a"))))  ;; 100 - 30
      (is (= 80 (first (sut/ledger-balance l "b"))))  ;; 50 + 30
      ;; Re-derive from WAL
      (let [sum-from-wal (reduce
                          (fn [acc ^Value v]
                            (case (.type v)
                              "ledger_create"
                              (assoc acc (get (.data v) :owner) 0)
                              "ledger_deposit"
                              (update acc (get (.data v) :owner)
                                      + (Long/parseLong (get (.data v) :amount)))
                              "ledger_withdraw"
                              (update acc (get (.data v) :owner)
                                      - (Long/parseLong (get (.data v) :amount)))
                              "ledger_transfer"
                              (let [amt (Long/parseLong (get (.data v) :amount))]
                                (-> acc
                                    (update (get (.data v) :from) - amt)
                                    (update (get (.data v) :to) + amt)))
                              acc))
                          {}
                          (sut/ledger-entries l))]
        (doseq [[owner want] sum-from-wal]
          (let [got (first (sut/ledger-balance l owner))]
            (is got (str owner " missing from state"))
            (is (= want got) (str owner ": state=" got ", WAL-derived=" want))))))))

(deftest test-property-wal-entries-round-trip
  ;; Invariant: Entries round-trip through JSON
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "a")
      (sut/ledger-deposit! l "a" 100)
      (sut/ledger-withdraw! l "a" 30)
      (doseq [^Value v (sut/ledger-entries l)]
        (let [json-str (json/write-str (sut/value->json-map v))
              m        (json/read-str json-str :key-fn keyword)
              v2       (Value. (:id m) (:type m) (:data m))]
          (is (= (.id v) (.id v2)))
          (is (= (.type v) (.type v2)))
          (doseq [[k val] (.data v)]
            (is (= val (get (.data v2) k))
                (str "round-trip data mismatch for key " k))))))))

;; ═══════════════════════════════════════════════════════════════
;; Edge case tests
;; ═══════════════════════════════════════════════════════════════

(deftest test-transfer-all-balance
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "a")
      (sut/ledger-create! l "b")
      (sut/ledger-deposit! l "a" 500)
      (sut/ledger-transfer! l "a" "b" 500)
      (is (= 0 (first (sut/ledger-balance l "a"))))
      (is (= 500 (first (sut/ledger-balance l "b")))))))

(deftest test-many-accounts
  (with-ledger
    (fn [l]
      (let [n 100]
        (doseq [i (range 1 (inc n))]
          (let [name (str "user_" i)]
            (sut/ledger-create! l name)
            (sut/ledger-deposit! l name (* i 10))))
        (doseq [i (range 1 (inc n))]
          (let [name (str "user_" i)]
            (is (= (* i 10) (first (sut/ledger-balance l name)))
                (str name ": expected " (* i 10) ", got " (first (sut/ledger-balance l name))))))))))

(deftest test-large-amount
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "a")
      (sut/ledger-create! l "b")
      (let [big (long (/ Long/MAX_VALUE 2))]
        (sut/ledger-deposit! l "a" big)
        (sut/ledger-transfer! l "a" "b" big)
        (is (= 0 (first (sut/ledger-balance l "a"))))
        (is (= big (first (sut/ledger-balance l "b"))))
        (sut/ledger-deposit! l "b" big)
        (is (= (* 2 big) (first (sut/ledger-balance l "b"))))))))

(deftest test-reopen-empty-wal
  (with-ledger
    (fn [l]
      (is (= 0 (count (sut/ledger-balances l)))))))

(deftest test-balances-returns-copy
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "a")
      (let [b1 (sut/ledger-balances l)]
        (is (not (contains? b1 "x")))
        ;; Even if we could mutate (which we can't in Clojure),
        ;; the original should be unaffected
        (is (not (contains? (sut/ledger-balances l) "x")))))))

;; ═══════════════════════════════════════════════════════════════
;; Audit & History tests
;; ═══════════════════════════════════════════════════════════════

(deftest test-history
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "alice")
      (sut/ledger-create! l "bob")
      (sut/ledger-deposit! l "alice" 1000)
      (sut/ledger-transfer! l "alice" "bob" 300)
      (sut/ledger-withdraw! l "bob" 50)
      (let [alice-entries (sut/ledger-history l "alice")]
        (is (= 3 (count alice-entries)) "alice: expected 3 entries")) ;; create, deposit, transfer
      (let [bob-entries (sut/ledger-history l "bob")]
        (is (= 3 (count bob-entries)) "bob: expected 3 entries")) ;; create, transfer, withdraw
      (is (thrown? Exception (sut/ledger-history l "nobody"))))))

(deftest test-verify
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "alice")
      (sut/ledger-deposit! l "alice" 1000)
      ;; bob doesn't exist
      (is (thrown? Exception (sut/ledger-verify! l "bob")))
      (sut/ledger-create! l "bob")
      (sut/ledger-transfer! l "alice" "bob" 300)
      (is (nil? (sut/ledger-verify! l "alice")))
      (is (nil? (sut/ledger-verify! l "bob"))))))

(deftest test-audit
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "bank")
      (sut/ledger-deposit! l "bank" 50000)
      (doseq [i (range 20)]
        (let [name (str "u" i)]
          (sut/ledger-create! l name)
          (sut/ledger-transfer! l "bank" name (+ 100 (* i 10)))))
      (let [rng (java.util.Random. 42)]
        (dotimes [_ 100]
          (let [from (str "u" (.nextInt rng 20))
                to   (str "u" (.nextInt rng 20))]
            (when (not= from to)
              (let [[bal ok?] (sut/ledger-balance l from)]
                (when (and ok? (pos? bal))
                  (let [amt (inc (.nextInt rng (min bal 500)))]
                    (try (sut/ledger-transfer! l from to amt)
                         (catch Exception _)))))))))
      (let [failures (sut/ledger-audit l)]
        (is (empty? failures) (str "audit failures: " failures))))))

(deftest test-audit-detects-corruption
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "alice")
      (sut/ledger-deposit! l "alice" 1000)
      (is (empty? (sut/ledger-audit l)))
      ;; Corrupt in-memory state directly (bypass WAL)
      (let [lock (.lock l)]
        (.lock lock)
        (try
          (swap! (.balances-atom l) assoc "alice" 9999)
          (finally
            (.unlock lock))))
      (is (thrown? Exception (sut/ledger-verify! l "alice"))
          "verify should detect the corruption"))))

(deftest test-history-order
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "alice")
      (sut/ledger-deposit! l "alice" 100)
      (sut/ledger-deposit! l "alice" 200)
      (sut/ledger-withdraw! l "alice" 50)
      (let [entries (sut/ledger-history l "alice")]
        (is (= 4 (count entries)))
        (is (= "ledger_create" (.type ^Value (nth entries 0))))
        (is (= "ledger_deposit" (.type ^Value (nth entries 1))))
        (is (= "100" (get (.data ^Value (nth entries 1)) :amount)))
        (is (= "ledger_deposit" (.type ^Value (nth entries 2))))
        (is (= "200" (get (.data ^Value (nth entries 2)) :amount)))
        (is (= "ledger_withdraw" (.type ^Value (nth entries 3))))
        (is (= "50" (get (.data ^Value (nth entries 3)) :amount)))))))

;; ═══════════════════════════════════════════════════════════════
;; Concurrent tests
;; ═══════════════════════════════════════════════════════════════

(deftest test-concurrent-deposits
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "shared")
      (let [n     100
            pool  (Executors/newFixedThreadPool 8)
            cd    (java.util.concurrent.CountDownLatch. n)]
        (dotimes [_ n]
          (.submit pool ^Runnable
                   (fn []
                     (try
                       (sut/ledger-deposit! l "shared" 1)
                       (finally
                         (.countDown cd))))))
        (.await cd 10 TimeUnit/SECONDS)
        (.shutdown pool)
        (is (= n (first (sut/ledger-balance l "shared")))
            (str "expected " n " after " n " concurrent deposits"))))))

(deftest test-concurrent-mixed-ops
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "a")
      (sut/ledger-create! l "b")
      (sut/ledger-deposit! l "a" 10000)
      (sut/ledger-deposit! l "b" 10000)
      (let [n     200
            pool  (Executors/newFixedThreadPool 8)
            errs  (java.util.concurrent.ConcurrentLinkedQueue.)
            cd    (java.util.concurrent.CountDownLatch. n)]
        (dotimes [i n]
          (.submit pool ^Runnable
                   (fn []
                     (try
                       (case (mod i 3)
                         0 (sut/ledger-deposit! l "a" 1)
                         1 (sut/ledger-withdraw! l "b" 1)
                         2 (sut/ledger-transfer! l "b" "a" 1))
                       (catch Exception e
                         (.add errs (.getMessage e)))
                       (finally
                         (.countDown cd))))))
        (.await cd 10 TimeUnit/SECONDS)
        (.shutdown pool)
        (is (empty? (vec errs)) (str "unexpected errors: " (vec errs)))
        (let [a (first (sut/ledger-balance l "a"))
              b (first (sut/ledger-balance l "b"))]
          (is (= 20000 (+ a b))
              (str "sum invariant broken: a=" a ", b=" b ", sum=" (+ a b))))))))

(deftest test-property-concurrent-safety
  (with-ledger
    (fn [l]
      (sut/ledger-create! l "bank")
      (sut/ledger-deposit! l "bank" 1000000)
      (let [n    50
            pool (Executors/newFixedThreadPool 16)
            cd   (java.util.concurrent.CountDownLatch. n)]
        ;; Phase 1: create users and fund from bank
        (dotimes [i n]
          (.submit pool ^Runnable
                   (fn []
                     (try
                       (let [name (str "u" i)]
                         (sut/ledger-create! l name)
                         (sut/ledger-transfer! l "bank" name (+ 100 (rand-int 5000))))
                       (finally
                         (.countDown cd))))))
        (.await cd 10 TimeUnit/SECONDS)
        ;; Phase 2: random transfers between users
        (let [rng (java.util.Random. 99)
              n2  200
              cd2 (java.util.concurrent.CountDownLatch. n2)]
          (dotimes [_ n2]
            (.submit pool ^Runnable
                     (fn []
                       (try
                         (let [from (str "u" (.nextInt rng n))
                               to   (str "u" (.nextInt rng n))]
                           (when (not= from to)
                             (let [[bal ok?] (sut/ledger-balance l from)]
                               (when (and ok? (pos? bal))
                                 (let [amt (inc (.nextInt rng (min bal 1000)))]
                                   (try (sut/ledger-transfer! l from to amt)
                                        (catch Exception _)))))))
                         (finally
                           (.countDown cd2))))))
          (.await cd2 10 TimeUnit/SECONDS))
        (.shutdown pool)
        ;; Verify invariants
        (let [bals  (sut/ledger-balances l)
              sum   (reduce + (vals bals))]
          (doseq [[owner bal] bals]
            (is (not (neg? bal)) (str owner " has negative balance: " bal)))
          (is (= 1000000 sum)
              (str "conservation broken: sum=" sum ", expected=1000000")))))))

;; ═══════════════════════════════════════════════════════════════
;; Fuzz tests (simulated random operations)
;; ═══════════════════════════════════════════════════════════════

(defn- rng-from-seed
  "Deterministic PRNG from a seed vector."
  [seed]
  (let [a (reduce (fn [acc ^long b] (bit-xor (Long/rotateLeft acc 7) b))
                  (long 0) (take-nth 2 seed))
        b (reduce (fn [acc ^long b] (bit-xor (Long/rotateLeft acc 13) b))
                  (long 0) (take-nth 2 (rest seed)))]
    (java.util.Random. (bit-xor a (Long/rotateLeft b 17)))))

(defn- rand-run
  "Execute random ledger operations. Best-effort: may fail harmlessly.
   Returns set of created account names."
  [rng l created]
  (let [ops (+ 10 (.nextInt rng 100))
        created (atom created)]
    (dotimes [_ ops]
      (let [owners (vec (keys @created))]
        (case (.nextInt rng 4)
          0 ;; create
          (let [name (str "u" (.nextInt rng 100000))]
            (when-not (@created name)
              (try
                (sut/ledger-create! l name)
                (swap! created assoc name true)
                (catch Exception _))))
          1 ;; deposit
          (when (seq owners)
            (let [name   (nth owners (.nextInt rng (count owners)))
                  amount (inc (.nextInt rng 100000))]
              (try (sut/ledger-deposit! l name amount) (catch Exception _))))
          2 ;; withdraw
          (when (seq owners)
            (let [name (nth owners (.nextInt rng (count owners)))
                  [bal ok?] (sut/ledger-balance l name)]
              (when (and ok? (pos? bal))
                (let [amount (inc (.nextInt rng (min bal 10000)))]
                  (try (sut/ledger-withdraw! l name amount) (catch Exception _))))))
          3 ;; transfer
          (when (>= (count owners) 2)
            (let [i  (.nextInt rng (count owners))
                  j  (.nextInt rng (count owners))]
              (when (not= i j)
                (let [from (nth owners i)
                      to   (nth owners j)
                      [from-bal ok?] (sut/ledger-balance l from)]
                  (when (and ok? (pos? from-bal))
                    (let [amount (inc (.nextInt rng (min from-bal 10000)))]
                      (try (sut/ledger-transfer! l from to amount) (catch Exception _)))))))))))
    @created))

(deftest test-fuzz-ledger
  ;; Fuzz-style: random operations, verify invariants after each run
  (doseq [seed [[0] [1 2 3] [42 99 137] (range 20)]]
    (with-ledger
      (fn [l]
        (let [rng      (rng-from-seed seed)
              created  (atom {})
              expect   (atom {})
              ops      (+ 10 (.nextInt rng 100))]
          (dotimes [op ops]
            (let [owners (vec (keys @created))]
              (case (.nextInt rng 7)
                0 ;; create (may be duplicate)
                (let [name (str "u" (.nextInt rng 100000))]
                  (try
                    (sut/ledger-create! l name)
                    (if (@created name)
                      (is false (str "step " op ": duplicate create " name " should fail"))
                      (do (swap! created assoc name true)
                          (swap! expect assoc name 0)))
                    (catch Exception e
                      (when-not (@created name)
                        (is false (str "step " op ": create " name ": " (.getMessage e)))))))
                1 ;; deposit (always valid if account exists)
                (when (seq owners)
                  (let [name   (nth owners (.nextInt rng (count owners)))
                        amount (inc (.nextInt rng 100000))]
                    (try
                      (sut/ledger-deposit! l name amount)
                      (swap! expect update name + amount)
                      (catch Exception e
                        (is false (str "step " op ": deposit " name " " amount ": " (.getMessage e)))))))
                2 ;; deposit to non-existent (must fail)
                (let [name (str "nx" (.nextInt rng 100000))]
                  (when-not (@created name)
                    (let [amount (inc (.nextInt rng 100000))]
                      (is (thrown? Exception (sut/ledger-deposit! l name amount))
                          (str "step " op ": deposit to non-existent " name " should fail")))))
                3 ;; withdraw (may fail on insufficient balance)
                (when (seq owners)
                  (let [name   (nth owners (.nextInt rng (count owners)))
                        bal    (get @expect name)
                        amount (inc (.nextInt rng (+ bal 100)))]
                    (try
                      (sut/ledger-withdraw! l name amount)
                      (if (< bal amount)
                        (is false (str "step " op ": withdraw " name " " amount " should fail (bal=" bal ")"))
                        (swap! expect update name - amount))
                      (catch Exception e
                        (when (>= bal amount)
                          (is false (str "step " op ": withdraw " name " " amount ": " (.getMessage e))))))))
                4 ;; withdraw from non-existent (must fail)
                (let [name (str "nx" (.nextInt rng 100000))]
                  (when-not (@created name)
                    (let [amount (inc (.nextInt rng 100000))]
                      (is (thrown? Exception (sut/ledger-withdraw! l name amount))
                          (str "step " op ": withdraw from non-existent " name " should fail")))))
                5 ;; transfer (may fail on insufficient balance)
                (when (>= (count owners) 2)
                  (let [i  (.nextInt rng (count owners))
                        j  (.nextInt rng (count owners))]
                    (when (not= i j)
                      (let [from   (nth owners i)
                            to     (nth owners j)
                            from-bal (get @expect from)
                            amount (inc (.nextInt rng (+ from-bal 100)))]
                        (try
                          (sut/ledger-transfer! l from to amount)
                          (if (< from-bal amount)
                            (is false (str "step " op ": transfer " from "->" to " " amount " should fail (bal=" from-bal ")"))
                            (do (swap! expect update from - amount)
                                (swap! expect update to + amount)))
                          (catch Exception e
                            (when (>= from-bal amount)
                              (is false (str "step " op ": transfer " from "->" to " " amount ": " (.getMessage e))))))))))
                6 ;; invalid transfer: self or non-existent (must fail)
                (when (seq owners)
                  (let [from   (nth owners (.nextInt rng (count owners)))
                        amount (inc (.nextInt rng 100000))]
                    (if (zero? (.nextInt rng 2))
                      ;; self-transfer
                      (is (thrown? Exception (sut/ledger-transfer! l from from amount))
                          (str "step " op ": self-transfer " from " should fail"))
                      ;; transfer to non-existent
                      (let [to (str "nx" (.nextInt rng 100000))]
                        (when-not (@created to)
                          (is (thrown? Exception (sut/ledger-transfer! l from to amount))
                              (str "step " op ": transfer to non-existent " from "->" to " should fail"))))))))))
          ;; Verify all balances match expected
          (doseq [[owner want] @expect]
            (let [got (first (sut/ledger-balance l owner))]
              (is got (str owner " missing from state"))
              (is (= want got) (str owner ": expected " want ", got " got))))
          ;; Verify no negative balances
          (doseq [[owner bal] (sut/ledger-balances l)]
            (is (not (neg? bal)) (str owner " has negative balance: " bal))))))))

(deftest test-fuzz-crash-recovery
  (doseq [seed [[0] [1 2 3] [99 42 7] (range 15)]]
    (let [path   (temp-path)
          rng    (rng-from-seed seed)
          expect (atom {})]
      ;; First session: run random ops, track expected state
      (let [l (sut/ledger path)]
        (try
          (let [created (atom {})
                ops     (+ 10 (.nextInt rng 100))]
            (dotimes [_ ops]
              (let [owners (vec (keys @created))]
                (case (.nextInt rng 4)
                  0 ;; create
                  (let [name (str "u" (.nextInt rng 100000))]
                    (when-not (@created name)
                      (try
                        (sut/ledger-create! l name)
                        (swap! created assoc name true)
                        (swap! expect assoc name 0)
                        (catch Exception _))))
                  1 ;; deposit
                  (when (seq owners)
                    (let [name   (nth owners (.nextInt rng (count owners)))
                          amount (inc (.nextInt rng 100000))]
                      (try
                        (sut/ledger-deposit! l name amount)
                        (swap! expect update name + amount)
                        (catch Exception _))))
                  2 ;; withdraw
                  (when (seq owners)
                    (let [name (nth owners (.nextInt rng (count owners)))
                          bal  (get @expect name)]
                      (when (pos? bal)
                        (let [amount (inc (.nextInt rng (min bal 10000)))]
                          (try
                            (sut/ledger-withdraw! l name amount)
                            (swap! expect update name - amount)
                            (catch Exception _))))))
                  3 ;; transfer
                  (when (>= (count owners) 2)
                    (let [i  (.nextInt rng (count owners))
                          j  (.nextInt rng (count owners))]
                      (when (not= i j)
                        (let [from     (nth owners i)
                              to       (nth owners j)
                              from-bal (get @expect from)]
                          (when (pos? from-bal)
                            (let [amount (inc (.nextInt rng (min from-bal 10000)))]
                              (try
                                (sut/ledger-transfer! l from to amount)
                                (swap! expect update from - amount)
                                (swap! expect update to + amount)
                                (catch Exception _))))))))))))
          (finally
            (sut/ledger-close! l))))
      ;; Reopen: state must match
      (let [l2 (sut/ledger path)]
        (try
          (doseq [[owner want] @expect]
            (let [[got ok?] (sut/ledger-balance l2 owner)]
              (is ok? (str owner " missing after recovery"))
              (is (= want got) (str owner ": expected " want ", got " got " after recovery"))))
          (finally
            (sut/ledger-close! l2)
            (io/delete-file path true)))))))

(deftest test-fuzz-account-traceability
  ;; Every account's balance must be derivable from WAL entries
  (doseq [seed [[0] [42] [7 13 21]]]
    (with-ledger
      (fn [l]
        (let [rng (rng-from-seed seed)]
          (sut/ledger-create! l "bank")
          (sut/ledger-deposit! l "bank" 1000000)
          (rand-run rng l {"bank" true})
          (doseq [[name actual] (sut/ledger-balances l)]
            (let [derived (reduce
                          (fn [acc v]
                            (+ acc (sut/value-balance-delta v name)))
                          0
                          (sut/ledger-entries l))]
              (is (= derived actual)
                  (str name ": WAL-derived " derived " != ledger " actual)))))))))

;; ═══════════════════════════════════════════════════════════════
;; Simulation test (exchange scenario)
;; ═══════════════════════════════════════════════════════════════

(deftest test-simulation
  (let [exchange-init 100000000
        num-users     1000
        max-init-cap  10000
        total-rounds  5000
        transfer-cap  1000
        path          "sim-wal.jsonl"]
    (io/delete-file path true)
    (let [l (sut/ledger path)]
      (try
        (sut/ledger-create! l "exchange")
        (sut/ledger-deposit! l "exchange" exchange-init)

        (let [rng         (java.util.Random. 42)
              active      (atom [])
              active-set  (atom #{})
              init-cap    (atom {})]

          ;; Initialize users
          (dotimes [i num-users]
            (let [name (str "u" i)
                  cap  (+ 100 (.nextInt rng (- max-init-cap 100)))]
              (sut/ledger-create! l name)
              (sut/ledger-transfer! l "exchange" name cap)
              (swap! active conj name)
              (swap! active-set conj name)
              (swap! init-cap assoc name cap)))

          ;; Simulate trading rounds
          (dotimes [round total-rounds]
            ;; Mid-join: occasionally add new users
            (when (zero? (.nextInt rng 100))
              (let [name (str "mid-" (count @active))]
                (sut/ledger-create! l name)
                (let [cap (+ 100 (.nextInt rng (- max-init-cap 100)))]
                  (sut/ledger-transfer! l "exchange" name cap)
                  (swap! active conj name)
                  (swap! active-set conj name)
                  (swap! init-cap assoc name cap))))

            ;; Matches per round
            (let [matches (+ 30 (.nextInt rng 21))
                  actives @active]
              (when (>= (count actives) 2)
                (dotimes [_ matches]
                  (let [i    (.nextInt rng (count actives))
                        j    (.nextInt rng (count actives))
                        from (nth actives i)
                        to   (nth actives j)]
                    (when (not= from to)
                      (let [[from-bal ok?] (sut/ledger-balance l from)]
                        (when (and ok? (pos? from-bal))
                          (let [amt (inc (.nextInt rng (min from-bal transfer-cap)))]
                            (try (sut/ledger-transfer! l from to amt)
                                 (catch Exception _)))))))))))

          ;; Verify conservation
          (let [ex-bal (first (sut/ledger-balance l "exchange"))
                users-sum (reduce (fn [sum name]
                                    (+ sum (first (sut/ledger-balance l name))))
                                  0 @active)]
            (is (= exchange-init (+ ex-bal users-sum))
                (str "conservation broken: exchange=" ex-bal
                     " + user-sum=" users-sum
                     " = " (+ ex-bal users-sum)
                     " != " exchange-init))))

        ;; Crash recovery
        (let [before (sut/ledger-balances l)]
          (sut/ledger-close! l)
          (let [l2 (sut/ledger path)]
            (try
              (let [after (sut/ledger-balances l2)]
                (doseq [[owner want] before]
                  (is (contains? after owner) (str owner " missing after recovery"))
                  (is (= want (get after owner))
                      (str owner ": before=" want ", after=" (get after owner)))))
              (finally
                (sut/ledger-close! l2)
                (io/delete-file path true)))))))))

(deftest test-sim-property-sum
  ;; Conservation of funds PBT: sum always matches
  (let [path "sim-pbt-wal.jsonl"]
    (io/delete-file path true)
    (let [l (sut/ledger path)]
      (try
        (sut/ledger-create! l "bank")
        (sut/ledger-deposit! l "bank" 1000000)
        (let [rng    (java.util.Random. 99)
              sum    (atom 1000000)
              exists (atom #{"bank"})]
          (dotimes [i 5000]
            (case (.nextInt rng 3)
              0 ;; transfer between existing
              (let [names (vec @exists)]
                (when (>= (count names) 2)
                  (let [idx (shuffle (range (count names)))
                        a   (nth names (first idx))
                        b   (nth names (second idx))
                        [a-bal ok?] (sut/ledger-balance l a)]
                    (when (and ok? (pos? a-bal))
                      (let [amt (inc (.nextInt rng (min a-bal 5000)))]
                        (sut/ledger-transfer! l a b amt))))))
              1 ;; deposit to bank (external injection)
              (let [amt (inc (.nextInt rng 10000))]
                (try
                  (sut/ledger-deposit! l "bank" amt)
                  (swap! sum + amt)
                  (catch Exception _)))
              2 ;; new user funded from bank
              (let [name (str "u" (.nextInt rng 1000000))]
                (when-not (@exists name)
                  (try
                    (sut/ledger-create! l name)
                    (swap! exists conj name)
                    (let [[b-bal ok?] (sut/ledger-balance l "bank")]
                      (when (and ok? (pos? b-bal))
                        (let [amt (.nextInt rng (inc (min b-bal 10000)))]
                          (when (pos? amt)
                            (sut/ledger-transfer! l "bank" name amt)))))
                    (catch Exception _)))))
            ;; Check invariants
            (let [current-sum (reduce (fn [s name]
                                        (+ s (first (sut/ledger-balance l name))))
                                      0 @exists)]
              (is (= @sum current-sum)
                  (str "op " i ": sum=" current-sum ", expected=" @sum)))
            (doseq [name @exists]
              (let [[bal ok?] (sut/ledger-balance l name)]
                (when ok?
                  (is (not (neg? bal))
                      (str "op " i ": " name " balance=" bal " < 0")))))))
        (finally
          (sut/ledger-close! l)
          (io/delete-file path true))))))

;; ═══════════════════════════════════════════════════════════════
;; Pure function tests
;; ═══════════════════════════════════════════════════════════════

(deftest test-value-balance-delta
  (let [deposit  (Value. 1 "ledger_deposit" {:owner "a" :amount "100"})
        withdraw (Value. 2 "ledger_withdraw" {:owner "a" :amount "30"})
        transfer (Value. 3 "ledger_transfer" {:from "a" :to "b" :amount "50"})
        create   (Value. 0 "ledger_create" {:owner "a"})
        corrupt  (Value. 4 "ledger_deposit" {:owner "a" :amount "bad"})]
    (is (= 100 (sut/value-balance-delta deposit "a")))
    (is (= 0 (sut/value-balance-delta deposit "b")))
    (is (= -30 (sut/value-balance-delta withdraw "a")))
    (is (= -50 (sut/value-balance-delta transfer "a")))
    (is (= 50 (sut/value-balance-delta transfer "b")))
    (is (= 0 (sut/value-balance-delta transfer "c")))
    (is (= 0 (sut/value-balance-delta create "a")))
    (is (thrown? Exception (sut/value-balance-delta corrupt "a")))))
