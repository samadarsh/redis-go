<div align="center">

# redis-go

**A Redis-compatible key-value server written from scratch in Go: it speaks the real Redis protocol, persists to disk, expires keys, and reaches ~90% of real Redis's throughput.**

[![CI](https://github.com/samadarsh/redis-go/actions/workflows/ci.yml/badge.svg)](https://github.com/samadarsh/redis-go/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/go-1.22%2B-00ADD8?logo=go)
![Dependencies](https://img.shields.io/badge/dependencies-0-brightgreen)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

[Features](#features) · [Quick start](#quick-start) · [Benchmarks](#benchmarks) · [Design decisions](#design-decisions) · [Limitations](#limitations)

![redis-go demo: SET, GET, INCR, SET EX, TTL and KEYS in redis-cli, then a restart that keeps the data](demo.gif)

| ~90% | 2.3M req/s | 59 tests | 0 |
|:---:|:---:|:---:|:---:|
| of real Redis's throughput, without pipelining | pipelined `GET` on an M3 Pro | run with the race detector in CI | external dependencies |

</div>

## Contents

- [Features](#features)
- [Quick start](#quick-start)
- [Benchmarks](#benchmarks)
- [Architecture](#architecture)
- [Design decisions](#design-decisions)
- [How it's tested](#how-its-tested)
- [Persistence](#persistence)
- [redis-go vs Redis](#redis-go-vs-redis)
- [Limitations](#limitations)
- [What I learned](#what-i-learned)

## Features

- **Real Redis protocol (RESP2).** Works with the official `redis-cli`, `redis-benchmark` and any Redis client library. Pipelining included.
- **17 commands:** `GET` `SET` (with `EX`/`PX`/`PXAT`) `DEL` `EXISTS` `INCR` `DECR` `MSET` `MGET` `KEYS` (glob patterns) `EXPIRE` `PEXPIRE` `PEXPIREAT` `TTL` `PTTL` `PERSIST` `PING` `ECHO`.
- **Thread-safe store** shared by one goroutine per client, with atomic multi-key commands.
- **AOF persistence** with `always` / `everysec` / `no` fsync policies, crash recovery, and a file format that real Redis can load.
- **Key expiry**, lazy and active, using Redis's own sampling algorithm, and safe across restarts.
- **Graceful shutdown** on Ctrl+C: finishes in-flight commands, then flushes and fsyncs the AOF.
- **Tested hard:** 59 tests with the race detector in CI, fake clocks instead of sleeps, end-to-end restart tests, and replies checked against real Redis. Zero external dependencies.

## Quick start

You need [Go 1.22+](https://go.dev/dl) and `redis-cli` (`brew install redis` on macOS, `sudo apt install redis-tools` on Ubuntu).

```bash
git clone https://github.com/samadarsh/redis-go
cd redis-go
make run ADDR=:6380        # 6380, so it doesn't clash with a real Redis
```

In another terminal:

```console
$ redis-cli -p 6380 set name sam
OK
$ redis-cli -p 6380 set session abc EX 60
OK
$ redis-cli -p 6380 ttl session
(integer) 60
$ redis-cli -p 6380 incr visits
(integer) 1
```

Stop the server with Ctrl+C and start it again: the data is still there.

| Make target | What it does |
|---|---|
| `make run` | Start the server (`ADDR=:6380` to change the port) |
| `make test` | Run all tests with Go's race detector |
| `make check` | Everything CI runs: formatting, `go vet`, tests |
| `make bench` | Go micro-benchmarks, with allocation counts |
| `make compare` | Benchmark against real Redis with `redis-benchmark` (see [Benchmarks](#benchmarks)) |
| `make build` | Build a binary at `bin/redis-go` |

<details>
<summary><b>Command-line flags</b></summary>

| Flag | Default | Meaning |
|---|---|---|
| `-addr` | `:6379` | Address to listen on |
| `-aof` | `appendonly.aof` | Append-only file for persistence; `-aof ""` turns it off |
| `-appendfsync` | `everysec` | When to fsync the AOF: `always`, `everysec` or `no` |
| `-pprof` | off | Serve Go's profiler, e.g. `-pprof localhost:6060` |
| `-debug` | off | Log every connection and command |

</details>

<details>
<summary><b>All supported commands</b></summary>

| Command | What it does |
|---|---|
| `PING [message]` | Replies `PONG`, or echoes `message` back |
| `ECHO message` | Replies with `message` |
| `SET key value [EX seconds \| PX ms \| PXAT unix-ms]` | Stores `value` under `key`, optionally with an expiry. A plain `SET` removes any old expiry |
| `GET key` | Returns the value of `key`, or null if it doesn't exist |
| `DEL key [key ...]` | Deletes the keys and returns how many existed |
| `EXISTS key [key ...]` | Returns how many of the keys exist (repeats are counted each time) |
| `INCR key` / `DECR key` | Adds / subtracts 1 (a missing key starts at 0) and returns the result. Keeps the key's expiry |
| `MSET key value [key value ...]` | Sets several keys at once, atomically |
| `MGET key [key ...]` | Returns the values of several keys, with null for missing ones |
| `KEYS pattern` | Returns all keys matching a glob pattern (`*`, `?`, `[a-z]`, `[^a]`, `\`) |
| `EXPIRE key seconds` / `PEXPIRE key ms` | Makes `key` expire. Returns 1, or 0 if the key doesn't exist. Zero or negative deletes it |
| `PEXPIREAT key unix-ms` | Expires `key` at an absolute time (how the AOF stores every expiry) |
| `TTL key` / `PTTL key` | Time left in seconds / ms: `-1` if no expiry, `-2` if the key doesn't exist |
| `PERSIST key` | Removes the expiry. Returns 1 if there was one, else 0 |

</details>

## Benchmarks

**Machine:** Apple M3 Pro (5 performance + 6 efficiency cores), 18 GB RAM, macOS 26.5.2, Go 1.27.3, Redis 8.10.3.

**Method:** `redis-benchmark -t set,get,incr,mset -n 1000000 -c 50 -q`, without and with pipelining (`-P 16`). Both servers on localhost with persistence off. Reproduce it with `make compare`.

| Command | redis-go (req/s) | Redis (req/s) | % of Redis |
|---|---:|---:|---:|
| SET | 167,280 | 180,050 | 93% |
| GET | 167,140 | 189,214 | 88% |
| INCR | 163,827 | 188,111 | 87% |
| MSET (10 keys) | 153,162 | 173,220 | 88% |
| SET, pipelined (-P 16) | 1,838,235 | 1,636,661 | 112% |
| GET, pipelined (-P 16) | 2,314,814 | 2,217,295 | 104% |
| INCR, pipelined (-P 16) | 1,182,033 | 2,053,388 | 58% |
| MSET (10 keys), pipelined (-P 16) | 755,287 | 348,432 | 217% |

Results vary by about ±10% between runs.

**An honest reading of these numbers:**

- **Without pipelining, the network round trip is the limit, not the server.** Each client waits for a reply before sending the next request. CPU profiles show redis-go spends about 52% of its time in `read`/`write` system calls and most of the rest waking goroutines up; under 1% goes to parsing and running commands. Both servers end up near the same number.
- **Where redis-go "beats" Redis, it's because it uses several cores and Redis uses one.** redis-go ran at ~310% CPU; Redis executes all commands on a single thread. Per core, Redis is clearly more efficient. Even on one core, though, redis-go's pipelined MSET is 146% of Redis's, and I haven't measured why. My unverified guess is per-key bookkeeping that redis-go doesn't do (keyspace notifications, `WATCH` invalidation, replication), but I haven't profiled Redis to confirm it.
- **Why real Redis is faster where it counts:** it's tuned C with no garbage collector and its own event loop, so it has none of Go's goroutine-scheduling overhead. It also stores small integers *as integers*, so `INCR` does no string parsing. redis-go parses and re-formats the number while holding the store's global write lock, which is why pipelined `INCR` is at 58%: there, clients spend about 11% of CPU waiting for that lock.
- **Surprise:** redis-go on *one* core beat itself on 11 cores without pipelining (~212k vs ~167k req/s). Passing work between cores costs more than it saves when each request is this small.

<details>
<summary><b>Same benchmark with redis-go on one core</b> (<code>GOMAXPROCS=1</code>, like-for-like with single-threaded Redis)</summary>

| Command | redis-go, 1 core | Redis | % of Redis |
|---|---:|---:|---:|
| SET / GET / INCR | ~212,000 | ~207,000 | 101–105% |
| SET, pipelined | 1,751,313 | 1,623,376 | 108% |
| GET, pipelined | 1,818,181 | 2,202,643 | 83% |
| INCR, pipelined | 1,615,508 | 2,028,397 | 80% |

</details>

<details>
<summary><b>Go micro-benchmarks</b> (<code>make bench</code>)</summary>

| Benchmark | ns/op | allocs/op |
|---|---:|---:|
| Parse a `SET` command (`resp.Reader`) | 178 | 7 |
| Encode a reply, reusing the buffer | 37 | 0 |
| `Store.Set` | 17 | 0 |
| `Store.Get` | 43 | 0 |
| `Store.IncrBy` | 100 | 1 |
| `Store.Get`, 11 goroutines in parallel | 112 | 0 |

</details>

## Architecture

```mermaid
flowchart LR
    C["redis-cli or any Redis client"] -->|TCP, RESP| S["server: one goroutine per client"]
    S --> R["resp.Reader: bytes to command"]
    R --> H["Handler: command to store call"]
    H --> ST[("store: map + expiries, one RWMutex")]
    ST -->|logged under the lock| A["aof: append + fsync policy"]
    A --> F[("appendonly.aof")]
    H --> W["resp.Writer: reply to bytes"]
    W --> C
    E["active expiry, every 100 ms"] -.-> ST
```

| Package | Responsibility |
|---|---|
| [`internal/resp`](internal/resp) | Parse and encode the [RESP protocol](https://redis.io/docs/latest/develop/reference/protocol-spec/). Also accepts "inline" commands (`PING\r\n`) so `nc`/`telnet` work. |
| [`internal/server`](internal/server) | TCP server: accepts clients, reads commands, pipelining, graceful shutdown. `commands.go` maps command names to code. |
| [`internal/store`](internal/store) | The data: a thread-safe map with expiry, glob matching for `KEYS`, and change logging. |
| [`internal/aof`](internal/aof) | The append-only file: writing, fsync policies, and replay with crash recovery. |
| [`main.go`](main.go) | Flags, startup order (replay, then log, then serve), profiling, Ctrl+C handling. |

**What a command looks like on the wire.** `redis-cli set name redis` sends:

```
*3\r\n$3\r\nSET\r\n$4\r\nname\r\n$5\r\nredis\r\n
```

That's an array (`*`) of 3 bulk strings (`$`), each prefixed by its length. The server replies `+OK\r\n`. The AOF stores commands in exactly this format.

## Design decisions

### One `RWMutex` for the whole store, not sharding

A single lock makes every multi-key command (`MSET`, `MGET`, `DEL a b c`) atomic for free, and gives one global order of changes, which persistence depends on (next point). The cost shows up in the benchmarks: writes queue behind each other, and even reads don't scale perfectly, because every `RLock` updates one shared counter that all cores contend for. Sharding (N maps, each with its own lock, chosen by hashing the key) would scale better, but multi-key commands would then need to lock several shards in a fixed order, and the AOF would need its own way to keep changes in order. Redis avoids the whole question by running commands on one thread.

### The AOF is written under the store lock, before the change is applied

If a command changed memory first and wrote to the log afterwards, outside the lock, two clients could apply A then B but log B then A, and a restart would rebuild the wrong data with no error. Logging inside the lock keeps the file in exactly the order memory saw. Logging *before* applying means a failed disk write leaves memory unchanged, so memory and disk never disagree. A test fails if the store ever logs without holding the lock.

```mermaid
sequenceDiagram
    participant C as Client
    participant S as Store
    participant A as AOF
    C->>S: INCR counter
    Note over S: take the write lock
    S->>S: check expiry, parse, check for overflow
    S->>A: Append SET counter 11
    A-->>S: ok, bytes handed to the OS
    S->>S: apply the change to the map
    Note over S: release the lock
    S-->>C: (integer) 11
```

### `INCR` is logged as `SET`

The file records the result (`SET counter 11`), not the operation (`INCR counter`). Replaying a `SET` twice still gives 11, while replaying `INCR` twice adds 2, and the checks (is it a number? would it overflow?) already ran when the command first ran. If the key has an expiry, the deadline goes in the same command (`SET counter 11 PXAT <time>`), so a single log line is all replay needs.

### Expiry times are stored as absolute timestamps

`SET session abc EX 10` is logged as `SET session abc PXAT 1793727766481`, never as "in 10 seconds". Otherwise, a server that's down for an hour would replay "10 seconds from now" and bring an expired session back to life, and every restart would reset the countdown. While the file is loading, nothing expires (like Redis), so each command replays against exactly the state it originally saw. Normal expiry then removes whatever expired while the server was down.

### Lazy + active expiry

Lazy: every command treats an expired key as missing, which is cheap and exact but never frees keys nobody reads again. Active: 10 times a second, a background task samples 20 keys that have a TTL and deletes the expired ones, and goes again immediately if more than 25% were expired. That's Redis's own algorithm, and it frees memory without scanning every key. Read commands hold only a read lock, so they *hide* expired keys and never delete them. Deleting there would be a data race, and the race-detector tests check this.

## How it's tested

```bash
make check    # gofmt, go vet, and all 59 tests with -race (exactly what CI runs)
```

- **Race detector everywhere.** Every test runs with `-race`, including tests that hammer the store from many goroutines while the clock moves and active expiry runs.
- **Tests that are checked to work.** The key concurrency tests were verified by breaking the code on purpose (logging outside the lock, a non-atomic `MSET`, a racy `INCR`, deleting under a read lock) and confirming that each test fails.
- **A fake clock, no sleeps.** Expiry tests move time forward by hand, so "an hour of downtime" takes nanoseconds and edge cases (1 ms before a deadline, exactly on it) are exact.
- **End-to-end restarts.** Tests start a real TCP server with a real AOF, write data, shut down, start a *new* server on the same file and check every key, including keys that must stay expired.
- **Crash recovery.** The AOF is cut at every byte position inside its last command, and replay must keep everything before it.
- **Checked against real Redis.** Replies to about 60 commands and the results of `KEYS` glob patterns were compared with Redis 8.10.3, and the AOF was loaded into real Redis (see below). These comparisons were run by hand, not in CI.

## Persistence

Every command that changes data is appended to the AOF in RESP format, and on startup the file is replayed to rebuild the data.

**Choosing `-appendfsync`.** A command is handed to the operating system as soon as it runs, so if only the *server* crashes (even `kill -9`), nothing is lost. The policy decides when the OS is forced to write to the physical disk, which is what protects you from a power cut:

| Policy | fsync | Lost on a power cut | Speed |
|---|---|---|---|
| `always` | after every write command | nothing that was acknowledged | slowest: every write waits for the disk |
| `everysec` | once a second, in the background | about the last second of writes | fast (the Redis default) |
| `no` | never; the OS decides | whatever the OS hadn't written yet (often up to ~30 s on Linux) | fastest |

**If the server dies mid-write**, the last command in the file can be cut off. On startup, redis-go logs a warning, truncates the incomplete command and loads everything before it (like Redis's `aof-load-truncated yes`). Damage anywhere else is treated as corruption: the server refuses to start rather than run with silently missing data.

**Real Redis can load the file.** This was tested with Redis 8.10.3: it rebuilt exactly the same keys, values and TTLs as redis-go. Redis 7+ normally stores its AOF as several files in an `appendonlydir/` directory, listed in a manifest (a *base* file plus *incremental* files). When it finds a single old-style `appendonly.aof` instead, it **moves** that file into `appendonlydir/` as the base file and starts a new incremental file, so give it a copy:

```bash
mkdir /tmp/r && cp appendonly.aof /tmp/r/
redis-server --port 6399 --dir /tmp/r --appendonly yes --save ""
# log: "Successfully migrated an old-style AOF file (appendonly.aof) into the AOF directory"
```

## redis-go vs Redis

| | redis-go | Redis |
|---|---|---|
| Protocol | RESP2 | RESP2 and RESP3 |
| Data types | Strings | Strings, lists, hashes, sets, sorted sets, streams and more |
| Commands | 17 | Hundreds |
| Threading | One goroutine per client; one lock for the data | Commands run on one thread (optional I/O threads) |
| Persistence | AOF with `always` / `everysec` / `no` | AOF (multi-part) and RDB snapshots |
| AOF rewriting | No: the file grows forever | Yes (`BGREWRITEAOF`) |
| Expiry | Lazy + active, same sampling algorithm | Lazy + active |
| Replication, cluster, auth, TLS | No | Yes |
| Language | Go, standard library only | C |

## Limitations

redis-go is a learning project, not a production database. In particular:

- **Strings only.** No lists, hashes, sets or sorted sets yet.
- **The AOF is never compacted.** There's no `BGREWRITEAOF`, so the file only grows and startup gets slower over time.
- **`KEYS` scans every key** while holding a read lock, which blocks all writes until it finishes. There's no `SCAN`.
- **One global lock.** Writes don't scale across cores; pipelined `INCR` reaches 58% of Redis.
- **Some `SET` options are missing:** `NX`, `XX`, `GET`, `KEEPTTL` and `EXAT` return a syntax error.
- **No replication, clustering, authentication or TLS.** Don't expose it to an untrusted network.

## What I learned

- Concurrency bugs often don't crash. Logging outside the lock rebuilt the wrong data, but the race detector didn't flag it. I had to design tests that would catch it, and check that they actually do by breaking the code on purpose.
- Persistence is mostly about the order of operations and what happens on failure: when to fsync, what a cut-off write looks like, and why a log should record results rather than actions.
- Time is an input. An injectable clock made every expiry test instant and repeatable, and caught a replay bug that real sleeps would have hidden.
- Measure before optimising. I expected allocations to be the bottleneck; the profiler showed under 1% in allocation and about 50% in system calls.
- Real Redis is the best specification: comparing replies with it found behaviour I'd have guessed wrong (for example, repeating `SET k v EX 10 EX 30` is allowed).

## Roadmap

- [x] **1. TCP server.** One goroutine per client, graceful shutdown.
- [x] **2. RESP protocol.** Parse commands, encode replies, pipelining, malformed input.
- [x] **3. `SET` / `GET` / `DEL`.** A `map[string]string` protected by a `sync.RWMutex`.
- [x] **4. More commands.** `EXISTS`, `INCR`, `DECR`, `MGET`, `MSET`, `KEYS`.
- [x] **5. Persistence (AOF).** Append every write, replay on startup, fsync policies, crash recovery.
- [x] **6. Expiry.** `EXPIRE`, `TTL`, `SET key value EX 10`, lazy and active expiry.
- [x] **7. Benchmarks.** Compared with real Redis using `redis-benchmark`, profiled with pprof.

**Next ideas:** AOF rewriting (`BGREWRITEAOF`), sharded store, `SCAN`, lists and hashes, `PUBLISH`/`SUBSCRIBE`, replication.

## Learning resources

- [Build Your Own Redis from Scratch (Go)](https://www.build-redis-from-scratch.dev/)
- [RESP protocol spec](https://redis.io/docs/latest/develop/reference/protocol-spec/)
- [Redis persistence](https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/)
- [Redis command reference](https://redis.io/docs/latest/commands/)
- [build-your-own-x](https://github.com/codecrafters-io/build-your-own-x), where this project idea came from

## License

[MIT](LICENSE). Issues and suggestions are welcome.
