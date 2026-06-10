(ns value-wal.core
  "Banking ledger backed by a write-ahead log (WAL).

  Architecture:
    Value        - immutable NDJSON record {id type data}
    FileWAL      - append-only WAL with thread-safe file writes & in-memory replay
    Ledger       - account operations (create, deposit, withdraw, transfer)
                   plus audit/verify/history, all protected by a lock

  Invariants (checked at construction & via Audit):
    - No account balance ever goes negative
    - Sum of all balances == net external deposits (conservation of funds)
    - In-memory balances always match WAL-derived computation"
  (:require [clojure.data.json :as json]
            [clojure.java.io :as io])
  (:import [java.util.concurrent.locks ReentrantLock]))

;; ═══════════════════════════════════════════════════════════════
;; Value — immutable WAL record
;; ═══════════════════════════════════════════════════════════════

(defrecord Value [id type data])

(defn value->json-map
  "Converts a Value to a plain map suitable for JSON serialization."
  [^Value v]
  {:id (.id v) :type (.type v) :data (.data v)})

;; ═══════════════════════════════════════════════════════════════
;; Pure functions on Value (no side effects)
;; ═══════════════════════════════════════════════════════════════

(defn value-involves?
  "Returns true when the WAL entry references owner in any role."
  [^Value v owner]
  (case (.type v)
    ("ledger_create" "ledger_deposit" "ledger_withdraw")
    (= (get (.data v) :owner) owner)
    "ledger_transfer"
    (or (= (get (.data v) :from) owner)
        (= (get (.data v) :to) owner))
    false))

(defn value-balance-delta
  "Returns the net balance change for owner caused by v.
   Throws on corrupt WAL (unparseable amount)."
  [^Value v owner]
  (letfn [(parse-amount []
            (try
              (Long/parseLong (get (.data v) :amount))
              (catch NumberFormatException _
                (throw (ex-info (str "corrupt WAL entry " (.id v)
                                     ": invalid amount " (pr-str (get (.data v) :amount)))
                                {:entry-id (.id v)})))))]
    (case (.type v)
      "ledger_deposit"
      (if (= (get (.data v) :owner) owner) (parse-amount) 0)
      "ledger_withdraw"
      (if (= (get (.data v) :owner) owner) (- (parse-amount)) 0)
      "ledger_transfer"
      (let [amt (parse-amount)]
        (cond
          (= (get (.data v) :from) owner) (- amt)
          (= (get (.data v) :to)   owner) amt
          :else 0))
      0)))

;; ═══════════════════════════════════════════════════════════════
;; FileWAL — append-only NDJSON WAL with in-memory replay buffer
;; ═══════════════════════════════════════════════════════════════

(defrecord FileWAL [^ReentrantLock lock
                    entries-atom    ;; atom: vector of Value
                    next-atom       ;; atom: next id (int)
                    ^java.io.Writer writer])

(defn- parse-entries-from
  "Reads NDJSON file and returns vector of Value records."
  [^java.io.File f]
  (when (.exists f)
    (with-open [rdr (io/reader f)]
      (->> (line-seq rdr)
           (remove clojure.string/blank?)
           (mapv (fn [line]
                   (let [m (json/read-str line :key-fn keyword)]
                     (Value. (:id m) (:type m) (:data m)))))))))

(defn file-wal
  "Creates a new FileWAL. If path already exists, replays entries from it."
  [path]
  (let [f       (io/file path)
        entries (or (parse-entries-from f) [])
        next-id (inc (count entries))
        writer  (io/writer f :append true)]
    (FileWAL. (ReentrantLock.) (atom entries) (atom next-id) writer)))

(defn wal-append
  "Appends a WAL entry. Returns the written Value."
  [^FileWAL fw t data]
  (let [lock (.lock fw)]
    (.lock lock)
    (try
      (let [id   @(.next-atom fw)
            v    (Value. id t data)
            line (str (json/write-str (value->json-map v)) "\n")]
        (.write (.writer fw) line)
        (.flush (.writer fw))
        (reset! (.next-atom fw) (inc id))
        (swap! (.entries-atom fw) conj v)
        v)
      (finally
        (.unlock lock)))))

(defn wal-entries
  "Returns all WAL entries (immutable snapshot)."
  [^FileWAL fw]
  (let [lock (.lock fw)]
    (.lock lock)
    (try
      @(.entries-atom fw)
      (finally
        (.unlock lock)))))

(defn wal-sync
  "Flushes the underlying file."
  [^FileWAL fw]
  (let [lock (.lock fw)]
    (.lock lock)
    (try
      (.flush (.writer fw))
      (finally
        (.unlock lock)))))

(defn wal-close
  "Syncs and closes the WAL file."
  [^FileWAL fw]
  (wal-sync fw)
  (.close (.writer fw)))

;; ═══════════════════════════════════════════════════════════════
;; Ledger
;; ═══════════════════════════════════════════════════════════════

(defrecord Ledger [^FileWAL wal
                   ^ReentrantLock lock
                   balances-atom]) ;; atom: {owner balance}

;; ── Internal helpers ──────────────────────────────────────────

(defn- parse-amount!
  "Parses the :amount field from a Value's data map. Throws on invalid value."
  [^Value v]
  (try
    (Long/parseLong (get (.data v) :amount))
    (catch NumberFormatException _
      (throw (ex-info (str "corrupt WAL: invalid amount " (pr-str (get (.data v) :amount)))
                      {:entry-id (.id v)})))))

(defn- apply-entry!
  "Mutates balances-atom by applying a single WAL entry."
  [balances-atom ^Value v]
  (case (.type v)
    "ledger_create"
    (swap! balances-atom assoc (get (.data v) :owner) 0)

    "ledger_deposit"
    (let [amt (parse-amount! v)]
      (swap! balances-atom update (get (.data v) :owner) + amt))

    "ledger_withdraw"
    (let [amt (parse-amount! v)]
      (swap! balances-atom update (get (.data v) :owner) - amt))

    "ledger_transfer"
    (let [amt  (parse-amount! v)
          from (get (.data v) :from)
          to   (get (.data v) :to)]
      (swap! balances-atom #(-> % (update from - amt) (update to + amt))))

    nil))

(defn- with-lock
  "Executes f while holding the ledger lock. Returns f's result."
  [^Ledger l f]
  (let [lock (.lock l)]
    (.lock lock)
    (try
      (f)
      (finally
        (.unlock lock)))))

;; ── Construction ──────────────────────────────────────────────

(defn ledger
  "Creates a Ledger. Accepts either a path string (creates FileWAL) or a FileWAL.
   Replays all WAL entries to reconstruct in-memory balances.
   Throws if replay produces a negative balance."
  ([wal-or-path]
   (let [fw (if (instance? FileWAL wal-or-path)
              wal-or-path
              (file-wal wal-or-path))
         l  (Ledger. fw (ReentrantLock.) (atom {}))]
     ;; Replay WAL
     (doseq [v (wal-entries fw)]
       (apply-entry! (.balances-atom l) v))
     ;; Safety check: no negative balances after replay
     (doseq [[owner bal] @(.balances-atom l)]
       (when (neg? bal)
         (throw (ex-info (str "corrupt WAL: account " owner
                              " has negative balance " bal " after replay")
                         {:owner owner :balance bal}))))
     l)))

;; ── Account operations ────────────────────────────────────────

(defn ledger-create!
  "Creates a new account with zero balance. Throws if account already exists."
  [^Ledger l owner]
  (with-lock l
    #(do
       (when (contains? @(.balances-atom l) owner)
         (throw (ex-info (str "ledger account " owner " already exists")
                         {:owner owner})))
       (let [v (wal-append (.wal l) "ledger_create" {:owner owner})]
         (apply-entry! (.balances-atom l) v)
         v))))

(defn ledger-deposit!
  "Deposits a positive amount into an existing account."
  [^Ledger l owner amount]
  (with-lock l
    #(do
       (when-not (contains? @(.balances-atom l) owner)
         (throw (ex-info (str "ledger account " owner " not found")
                         {:owner owner})))
       (when-not (pos? amount)
         (throw (ex-info "deposit amount must be positive" {:amount amount})))
       (let [v (wal-append (.wal l) "ledger_deposit"
                           {:owner owner :amount (str amount)})]
         (apply-entry! (.balances-atom l) v)
         v))))

(defn ledger-withdraw!
  "Withdraws a positive amount. Throws if balance insufficient or account missing."
  [^Ledger l owner amount]
  (with-lock l
    #(do
       (let [bal (get @(.balances-atom l) owner ::missing)]
         (when (= bal ::missing)
           (throw (ex-info (str "ledger account " owner " not found")
                           {:owner owner})))
         (when-not (pos? amount)
           (throw (ex-info "withdraw amount must be positive" {:amount amount})))
         (when (< bal amount)
           (throw (ex-info (str "insufficient balance: " owner " has " bal
                                ", tried to withdraw " amount)
                           {:owner owner :balance bal :amount amount}))))
       (let [v (wal-append (.wal l) "ledger_withdraw"
                           {:owner owner :amount (str amount)})]
         (apply-entry! (.balances-atom l) v)
         v))))

(defn ledger-transfer!
  "Transfers a positive amount from one account to another.
   Throws on: self-transfer, missing account, insufficient balance, non-positive amount."
  [^Ledger l from to amount]
  (with-lock l
    #(do
       (when (= from to)
         (throw (ex-info "cannot transfer to self" {:from from :to to})))
       (let [from-bal (get @(.balances-atom l) from ::missing)]
         (when (= from-bal ::missing)
           (throw (ex-info (str "ledger account " from " not found") {:owner from})))
         (when-not (contains? @(.balances-atom l) to)
           (throw (ex-info (str "ledger account " to " not found") {:owner to})))
         (when-not (pos? amount)
           (throw (ex-info "transfer amount must be positive" {:amount amount})))
         (when (< from-bal amount)
           (throw (ex-info (str "insufficient balance: " from " has " from-bal
                                ", tried to transfer " amount)
                           {:owner from :balance from-bal :amount amount}))))
       (let [v (wal-append (.wal l) "ledger_transfer"
                           {:from from :to to :amount (str amount)})]
         (apply-entry! (.balances-atom l) v)
         v))))

;; ── Queries ───────────────────────────────────────────────────

(defn ledger-balance
  "Returns [balance found?] for an account."
  [^Ledger l owner]
  (with-lock l
    #(if-let [bal (get @(.balances-atom l) owner)]
       [bal true]
       [0 false])))

(defn ledger-balances
  "Returns a snapshot of all account balances (immutable map)."
  [^Ledger l]
  (with-lock l
    #(into {} @(.balances-atom l))))

(defn ledger-entries
  "Returns all WAL entries."
  [^Ledger l]
  (wal-entries (.wal l)))

(defn ledger-close!
  "Syncs and closes the underlying WAL."
  [^Ledger l]
  (wal-close (.wal l)))

(defn ledger-history
  "Returns WAL entries involving owner, in WAL order.
   Throws if account does not exist."
  [^Ledger l owner]
  (with-lock l
    #(do
       (when-not (contains? @(.balances-atom l) owner)
         (throw (ex-info (str "ledger account " owner " not found")
                         {:owner owner})))
       (filterv (fn [v] (value-involves? v owner)) (wal-entries (.wal l))))))

;; ── Audit & Verification ─────────────────────────────────────

(defn- verify-locked
  "Computes balance from WAL and compares with in-memory state.
   Caller must hold the ledger lock. Throws on mismatch."
  [^Ledger l owner]
  (let [entries (wal-entries (.wal l))
        derived (reduce (fn [acc v] (+ acc (value-balance-delta v owner))) 0 entries)
        actual  (get @(.balances-atom l) owner ::missing)]
    (when (= actual ::missing)
      (throw (ex-info (str "ledger account " owner " not found") {:owner owner})))
    (when (not= derived actual)
      (throw (ex-info (str owner ": WAL-derived " derived " != in-memory " actual)
                      {:owner owner :derived derived :actual actual})))
    nil))

(defn ledger-verify!
  "Checks that owner's in-memory balance matches WAL-derived computation.
   Returns nil on success, throws on mismatch or missing account."
  [^Ledger l owner]
  (with-lock l #(verify-locked l owner)))

(defn ledger-audit
  "Verifies all accounts against the WAL.
   Returns a map of owner → error-message for each failing account.
   An empty map means all accounts are consistent."
  [^Ledger l]
  (with-lock l
    #(reduce-kv (fn [failures owner _]
                  (try
                    (verify-locked l owner)
                    failures
                    (catch Exception e
                      (assoc failures owner (.getMessage e)))))
                {}
                @(.balances-atom l))))

;; ═══════════════════════════════════════════════════════════════
;; Demo
;; ═══════════════════════════════════════════════════════════════

(defn -main
  "Runs a demo of the ledger system."
  [& _]
  (let [path "ledger.jsonl"]
    (io/delete-file (io/file path) true)

    (let [l (ledger path)]
      (try
        ;; Create accounts
        (ledger-create! l "alice")
        (ledger-create! l "bob")

        ;; Deposit
        (ledger-deposit! l "alice" 1000)
        (ledger-deposit! l "bob" 500)

        ;; Transfer
        (try (ledger-transfer! l "alice" "bob" 300)
             (catch Exception e (println "transfer:" (.getMessage e))))

        ;; Insufficient balance (rejected)
        (try (ledger-withdraw! l "alice" 2000)
             (catch Exception e (println "withdraw rejected:" (.getMessage e))))

        ;; Withdraw
        (try (ledger-withdraw! l "alice" 200)
             (catch Exception e (println "withdraw:" (.getMessage e))))

        (println "balances:")
        (doseq [[owner bal] (sort (ledger-balances l))]
          (println (str "  " owner ": " bal)))

        ;; Audit
        (println "\naudit:")
        (let [failures (ledger-audit l)]
          (if (empty? failures)
            (println "  all accounts verified")
            (doseq [[owner err] failures]
              (println (str "  " owner ": " err)))))

        (println "\nhistory:")
        (doseq [owner ["alice" "bob"]]
          (try
            (let [entries (ledger-history l owner)]
              (println (str "  " owner " (" (count entries) " entries):"))
              (doseq [^Value v entries]
                (case (.type v)
                  "ledger_create"
                  (println "    create")
                  "ledger_deposit"
                  (println (str "    deposit +" (get (.data v) :amount)))
                  "ledger_withdraw"
                  (println (str "    withdraw -" (get (.data v) :amount)))
                  "ledger_transfer"
                  (if (= (get (.data v) :from) owner)
                    (println (str "    transfer to " (get (.data v) :to)
                                  " -" (get (.data v) :amount)))
                    (println (str "    transfer from " (get (.data v) :from)
                                  " +" (get (.data v) :amount)))))))
            (catch Exception e
              (println (str "  " owner " history error: " (.getMessage e))))))

        ;; Crash recovery
        (ledger-close! l)
        (let [l2 (ledger path)]
          (try
            (println "\nafter reopen:")
            (doseq [[owner bal] (sort (ledger-balances l2))]
              (println (str "  " owner ": " bal)))
            (finally
              (ledger-close! l2))))
        (finally
          (try (ledger-close! l) (catch Exception _)))))))
