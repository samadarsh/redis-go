#!/usr/bin/env bash
#
# Benchmark redis-go against real Redis with redis-benchmark, and print the
# results as a markdown table.
#
#   ./scripts/benchmark.sh            (or: make compare)
#
# Both servers run with persistence turned off, on localhost, and are shut
# down at the end, even if you press Ctrl+C or something fails.
#
# Settings can be changed with environment variables:
#   GO_PORT=6380 REDIS_PORT=6379 REQUESTS=100000 CLIENTS=50 PIPELINE=16
#
# The script never stops a server it didn't start: if a port is already in
# use, it tells you and exits.

set -euo pipefail

GO_PORT="${GO_PORT:-6380}"
REDIS_PORT="${REDIS_PORT:-6379}"
REQUESTS="${REQUESTS:-100000}"
CLIENTS="${CLIENTS:-50}"
PIPELINE="${PIPELINE:-16}"
TESTS="set,get,incr,mset"

cd "$(dirname "$0")/.."

for tool in go redis-server redis-benchmark redis-cli; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "error: '$tool' not found. On macOS: brew install go redis" >&2
		exit 1
	fi
done

# port_in_use uses bash's built-in /dev/tcp, so it needs no extra tools.
port_in_use() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

for port in "$GO_PORT" "$REDIS_PORT"; do
	if port_in_use "$port"; then
		echo "error: port $port is already in use. Stop that server, or choose other ports:" >&2
		echo "  GO_PORT=6390 REDIS_PORT=6389 $0" >&2
		exit 1
	fi
done

TMP="$(mktemp -d)"
GO_PID=""
REDIS_PID=""

# cleanup runs on every exit: normal, error (set -e) or Ctrl+C.
cleanup() {
	for pid in "$GO_PID" "$REDIS_PID"; do
		if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
			kill "$pid" 2>/dev/null || true
			wait "$pid" 2>/dev/null || true
		fi
	done
	rm -rf "$TMP"
}
trap cleanup EXIT
trap 'echo; echo "interrupted, shutting down both servers..." >&2; exit 130' INT TERM

echo "Building redis-go..." >&2
go build -o "$TMP/redis-go" .

echo "Starting redis-go on :$GO_PORT and Redis on :$REDIS_PORT (no persistence)..." >&2
"$TMP/redis-go" -addr "127.0.0.1:$GO_PORT" -aof "" >"$TMP/redis-go.log" 2>&1 &
GO_PID=$!
redis-server --port "$REDIS_PORT" --bind 127.0.0.1 --save "" --appendonly no \
	--dir "$TMP" --daemonize no >"$TMP/redis.log" 2>&1 &
REDIS_PID=$!

wait_ready() { # port pid name log
	for _ in $(seq 100); do
		if redis-cli -p "$1" ping >/dev/null 2>&1; then return 0; fi
		if ! kill -0 "$2" 2>/dev/null; then
			echo "error: $3 exited during startup. Its log:" >&2
			cat "$4" >&2
			exit 1
		fi
		sleep 0.1
	done
	echo "error: $3 did not start within 10 seconds" >&2
	exit 1
}
wait_ready "$GO_PORT" "$GO_PID" redis-go "$TMP/redis-go.log"
wait_ready "$REDIS_PORT" "$REDIS_PID" redis-server "$TMP/redis.log"

# bench PORT PIPELINE LABEL_SUFFIX: prints "COMMAND|REQUESTS_PER_SECOND" lines.
# -q prints a progress line ending in \r, then a final line per test, like
#   SET: 163934.42 requests per second, p50=0.159 msec
bench() {
	local out
	out="$(redis-benchmark -h 127.0.0.1 -p "$1" -t "$TESTS" -n "$REQUESTS" -c "$CLIENTS" -P "$2" -q 2>&1 |
		tr '\r' '\n' | grep 'requests per second' || true)"
	if [ -z "$out" ]; then
		echo "error: redis-benchmark produced no results for port $1" >&2
		exit 1
	fi
	echo "$out" | sed -E "s/^([^:]+): ([0-9.]+) requests per second.*/\1$3|\2/"
}

for target in go redis; do
	port="$GO_PORT"
	[ "$target" = redis ] && port="$REDIS_PORT"
	echo "Benchmarking $target (pipeline 1, then $PIPELINE)..." >&2
	{
		bench "$port" 1 ""
		bench "$port" "$PIPELINE" ", pipelined (-P $PIPELINE)"
	} >"$TMP/$target.txt"
done

# Machine details, for the README.
if [ "$(uname)" = Darwin ]; then
	chip="$(sysctl -n machdep.cpu.brand_string)"
	ram="$(($(sysctl -n hw.memsize) / 1024 / 1024 / 1024)) GB"
	os="macOS $(sw_vers -productVersion)"
else
	chip="$(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2 | sed 's/^ //')"
	ram="$(awk '/MemTotal/ {printf "%d GB", $2/1024/1024}' /proc/meminfo)"
	os="$(uname -sr)"
fi

echo
echo "**Machine:** $chip, $ram RAM, $os, $(go version | awk '{print $3}'), $(redis-server --version | awk '{print $3}' | sed 's/v=/Redis /')"
echo
echo "\`redis-benchmark -t $TESTS -n $REQUESTS -c $CLIENTS -q\`, both servers on localhost with persistence off."
echo
awk -F'|' '
	# commas(1234567.8) -> "1,234,567": take 3 digits at a time from the right.
	function commas(n,   s, out) {
		s = sprintf("%d", n); out = ""
		while (length(s) > 3) { out = "," substr(s, length(s) - 2) out; s = substr(s, 1, length(s) - 3) }
		return s out
	}
	NR == FNR { redis[$1] = $2; next }
	FNR == 1 {
		print "| Command | redis-go (req/s) | Redis (req/s) | % of Redis |"
		print "|---|---:|---:|---:|"
	}
	{ printf "| %s | %s | %s | %d%% |\n", $1, commas($2), commas(redis[$1]), ($2 / redis[$1]) * 100 + 0.5 }
' "$TMP/redis.txt" "$TMP/go.txt"
