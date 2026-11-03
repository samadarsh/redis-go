# Demo recording for the README, made with charmbracelet/vhs.
#
#   brew install vhs     (also installs ttyd and ffmpeg)
#   vhs demo.tape        (writes demo.gif; takes about a minute)
#
# Port 6380 must be free while recording.

Output demo.gif
Require redis-cli
Require go

Set Shell "bash"
Set FontSize 18
Set Width 1200
Set Height 720
Set Padding 24
Set TypingSpeed 45ms
Set Theme "Catppuccin Mocha"

# Setup, not recorded: build the server and start with an empty AOF.
Hide
Type "go build -o /tmp/redis-go-demo . && rm -f /tmp/redis-go-demo.aof && export PS1='$ ' && clear"
Enter
Sleep 3s
Show

Type "# Start redis-go with persistence on"
Enter
Type "/tmp/redis-go-demo -addr :6380 -aof /tmp/redis-go-demo.aof &"
Enter
Sleep 1.5s

Type "redis-cli -p 6380 SET name sam"
Enter
Sleep 700ms
Type "redis-cli -p 6380 GET name"
Enter
Sleep 700ms
Type "redis-cli -p 6380 INCR visits"
Enter
Sleep 500ms
Type "redis-cli -p 6380 INCR visits"
Enter
Sleep 700ms
Type "redis-cli -p 6380 SET session abc EX 60"
Enter
Sleep 700ms
Type "redis-cli -p 6380 TTL session"
Enter
Sleep 700ms
Type "redis-cli -p 6380 KEYS '*'"
Enter
Sleep 1.5s

Type "# Stop the server (graceful shutdown flushes the AOF)..."
Enter
Type "kill %1; wait"
Enter
Sleep 1.5s

Type "# ...and start it again: the data is replayed from disk"
Enter
Type "/tmp/redis-go-demo -addr :6380 -aof /tmp/redis-go-demo.aof &"
Enter
Sleep 1.5s
Type "redis-cli -p 6380 GET name"
Enter
Sleep 700ms
Type "redis-cli -p 6380 GET visits"
Enter
Sleep 700ms
Type "redis-cli -p 6380 TTL session"
Enter
Sleep 3s

# Cleanup, not recorded.
Hide
Type "kill %1; wait; rm -f /tmp/redis-go-demo /tmp/redis-go-demo.aof"
Enter
Sleep 1s
