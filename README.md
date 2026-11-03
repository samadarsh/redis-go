# redis-go

A Redis-compatible in-memory key-value server, built from scratch in Go.

It speaks the real Redis protocol (RESP), so the official `redis-cli`,
`redis-benchmark` and any Redis client library can talk to it.

```console
$ redis-cli -p 6380 ping
PONG
$ redis-cli -p 6380 set name redis
OK
$ redis-cli -p 6380 get name
"redis"
```

> 🚧 **Work in progress.** I'm building this to learn how databases and
> network servers work under the hood. Progress is tracked in the
> [roadmap](#roadmap) below.

## Quick start

You need [Go 1.22+](https://go.dev/dl). To test it you'll also want
`redis-cli` (`brew install redis` on macOS, `sudo apt install redis-tools`
on Ubuntu).

```bash
git clone https://github.com/samadarsh/redis-go
cd redis-go
make run ADDR=:6380        # use 6380 so it doesn't clash with a real Redis
```

In another terminal:

```bash
redis-cli -p 6380 ping
```

You can also talk to it by hand, without `redis-cli`:

```bash
printf 'PING\r\n' | nc localhost 6380
```

| Command | What it does |
|---|---|
| `make run` | Start the server (`ADDR=:6380` to change the port, add `-debug` via `go run . -debug` to log every command) |
| `make test` | Run all tests with Go's race detector |
| `make check` | Everything CI runs: formatting, `go vet`, tests |
| `make build` | Build a binary at `bin/redis-go` |

## Supported commands

| Command | What it does |
|---|---|
| `PING [message]` | Replies `PONG`, or echoes `message` back |
| `ECHO message` | Replies with `message` |
| `SET key value` | Stores `value` under `key`, replacing any old value |
| `GET key` | Returns the value of `key`, or null if it doesn't exist |
| `DEL key [key ...]` | Deletes the keys and returns how many existed |
| `EXISTS key [key ...]` | Returns how many of the keys exist (repeats are counted each time) |
| `INCR key` | Adds 1 to the integer in `key` (a missing key starts at 0) and returns the result |
| `DECR key` | Subtracts 1 from the integer in `key` (a missing key starts at 0) and returns the result |
| `MSET key value [key value ...]` | Sets several keys at once, atomically |
| `MGET key [key ...]` | Returns the values of several keys, with null for missing ones |
| `KEYS pattern` | Returns all keys matching a glob pattern (`*`, `?`, `[a-z]`, `[^a]`, `\`) |

## Persistence

Data survives a restart. Every command that changes data is appended to an
**append-only file** (AOF) in the same RESP format a client sends, and on
startup the file is replayed to rebuild the data. The file format is
compatible with real Redis, which can load it.

```console
$ redis-cli -p 6380 set name sam
OK
# Ctrl+C the server, start it again, then:
$ redis-cli -p 6380 get name
"sam"
```

| Flag | Default | Meaning |
|---|---|---|
| `-aof path` | `appendonly.aof` | Where to keep the AOF. `-aof ""` turns persistence off. |
| `-appendfsync policy` | `everysec` | How often to fsync the file: `always`, `everysec` or `no`. |

**Choosing `-appendfsync`.** A command is handed to the operating system
as soon as it runs, so if only the *server* crashes, nothing is lost. The
policy decides when the OS is forced to write the data to the physical
disk, which is what protects you from a power cut or a kernel crash:

| Policy | fsync | Lost on a power cut | Speed |
|---|---|---|---|
| `always` | after every write command | nothing that was acknowledged | slowest: every write waits for the disk |
| `everysec` | once a second, in the background | about the last second of writes | fast (the Redis default) |
| `no` | never; the OS decides | whatever the OS hadn't written yet (often up to ~30 s on Linux) | fastest |

**If the server dies mid-write**, the last command in the file can be cut
off. On startup, redis-go logs a warning, truncates the incomplete command
and loads everything before it (like Redis's `aof-load-truncated yes`).
Damage anywhere else in the file is treated as real corruption: the server
refuses to start rather than run with silently missing data.

## How it works

```
 redis-cli ──TCP──▶ server.Serve ──▶ one goroutine per client (handleConn)
                                        │
                     ┌──────────────────┼───────────────────┐
                     ▼                  ▼                   ▼
               resp.Reader        server.Handler       resp.Writer
           bytes → command      "SET k v" → store     reply → bytes
                                        │
                                        ▼
                                   store.Store
                              (map + RWMutex, shared)
```

| Package | Responsibility |
|---|---|
| [`internal/resp`](internal/resp) | Parse and encode the [RESP protocol](https://redis.io/docs/latest/develop/reference/protocol-spec/). Also accepts "inline" commands (`PING\r\n`) so `nc`/`telnet` work. |
| [`internal/server`](internal/server) | TCP server: accepts clients, reads commands, supports pipelining, graceful shutdown. `commands.go` maps command names to code. |
| [`internal/store`](internal/store) | The actual data: a thread-safe key-value map. |
| [`main.go`](main.go) | Flags, logging, Ctrl+C handling. |

**What a command looks like on the wire.** `redis-cli set name redis` sends:

```
*3\r\n$3\r\nSET\r\n$4\r\nname\r\n$5\r\nredis\r\n
```

That is an array (`*`) of 3 bulk strings (`$`), each prefixed by its
length. The server replies `+OK\r\n`.

## Roadmap

Each milestone ends with something you can try in `redis-cli`. Tests for
unfinished parts **skip themselves** and start running automatically once
the code exists, so `make test` always tells you where you are.

- [x] **1. TCP server.** Listen on a port, one goroutine per client, graceful shutdown. *Done when `redis-cli ping` replies `PONG`.*
- [x] **2. RESP protocol.** Parse commands, encode replies, handle pipelining and malformed input.
- [x] **3. `SET` / `GET` / `DEL`.** Implement [`internal/store/store.go`](internal/store/store.go): a `map[string]string` protected by a `sync.RWMutex`. *Done when `make test` shows no skipped store tests.*
- [x] **4. More commands.** `EXISTS`, `INCR`, `DECR`, `MGET`, `MSET`, `KEYS`. Add them in [`internal/server/commands.go`](internal/server/commands.go), with tests.
- [x] **5. Persistence (AOF).** Append every write command to a file, and replay it on startup so data survives a restart. *Done when you can `SET`, restart the server, and `GET` still works.*
- [ ] **6. Expiry.** `EXPIRE`, `TTL`, `SET key value EX 10`. Lazy deletion on read, plus a background sweep.
- [ ] **7. Benchmark and write it up.** Compare with real Redis using `redis-benchmark`, add the numbers here.

**Stretch goals:** lists (`LPUSH`/`LRANGE`), hashes (`HSET`/`HGET`),
`PUBLISH`/`SUBSCRIBE`, RDB snapshots, `INFO`, primary-replica replication.

## Benchmarks

Early numbers, `PING` only, on a cloud VM (`redis-benchmark -t ping -n 20000 -q`):

| | requests/sec |
|---|---|
| redis-go, `PING_INLINE` | ~73,000 |
| redis-go, `PING_MBULK` | ~70,000 |

*Full comparison with real Redis coming in milestone 7.*

## Learning resources

- [Build Your Own Redis from Scratch (Go)](https://www.build-redis-from-scratch.dev/)
- [RESP protocol spec](https://redis.io/docs/latest/develop/reference/protocol-spec/)
- [Redis command reference](https://redis.io/docs/latest/commands/)
- [build-your-own-x](https://github.com/codecrafters-io/build-your-own-x), where this project idea came from

## License

[MIT](LICENSE)
