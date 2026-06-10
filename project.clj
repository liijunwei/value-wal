(defproject value-wal "0.1.0-SNAPSHOT"
  :description "Banking ledger backed by a write-ahead log"
  :dependencies [[org.clojure/clojure "1.12.0"]
                 [org.clojure/data.json "2.5.1"]]
  :main value-wal.core
  :source-paths ["src"]
  :test-paths ["test"])
