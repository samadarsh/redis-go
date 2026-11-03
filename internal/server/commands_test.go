package server

import (
	"reflect"
	"strings"
	"testing"

	"github.com/samadarsh/redis-go/internal/resp"
	"github.com/samadarsh/redis-go/internal/store"
)

func TestHandle(t *testing.T) {
	h := NewHandler(store.New())

	tests := []struct {
		args []string
		want resp.Value
	}{
		{[]string{"PING"}, resp.NewSimpleString("PONG")},
		{[]string{"ping"}, resp.NewSimpleString("PONG")}, // case-insensitive
		{[]string{"PING", "hello"}, resp.NewBulkString("hello")},
		{[]string{"PING", "a", "b"}, resp.NewError("ERR wrong number of arguments for 'ping' command")},
		{[]string{"ECHO", "hi there"}, resp.NewBulkString("hi there")},
		{[]string{"ECHO"}, resp.NewError("ERR wrong number of arguments for 'echo' command")},
		{[]string{"COMMAND", "DOCS"}, resp.NewArray()},
		{[]string{"NOPE", "x"}, resp.NewError("ERR unknown command 'NOPE'")},
		{[]string{"SET", "k"}, resp.NewError("ERR wrong number of arguments for 'set' command")},
		{[]string{"GET"}, resp.NewError("ERR wrong number of arguments for 'get' command")},
		{[]string{"DEL"}, resp.NewError("ERR wrong number of arguments for 'del' command")},
		{[]string{"EXISTS"}, resp.NewError("ERR wrong number of arguments for 'exists' command")},
		{[]string{"exists", "missing"}, resp.NewInteger(0)}, // case-insensitive, empty store
		{nil, resp.NewError("ERR empty command")},
	}

	for _, tt := range tests {
		if got := h.Handle(tt.args); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Handle(%q) = %#v, want %#v", tt.args, got, tt.want)
		}
	}
}

// Skips until you implement the store (milestone 3).
func TestSetGetDelCommands(t *testing.T) {
	h := NewHandler(store.New())

	reply := h.Handle([]string{"SET", "name", "redis"})
	if reply.Type == resp.Error && strings.Contains(reply.Str, "not implemented") {
		t.Skip("milestone 3: store not implemented yet")
	}

	steps := []struct {
		args []string
		want resp.Value
	}{
		{[]string{"GET", "name"}, resp.NewBulkString("redis")},
		{[]string{"GET", "missing"}, resp.NullBulkString()},
		{[]string{"DEL", "name", "missing"}, resp.NewInteger(1)},
		{[]string{"GET", "name"}, resp.NullBulkString()},
	}
	if !reflect.DeepEqual(reply, resp.OK()) {
		t.Fatalf("SET reply = %#v, want OK", reply)
	}
	for _, s := range steps {
		if got := h.Handle(s.args); !reflect.DeepEqual(got, s.want) {
			t.Errorf("Handle(%q) = %#v, want %#v", s.args, got, s.want)
		}
	}
}

func TestExistsCommand(t *testing.T) {
	h := NewHandler(store.New())
	if reply := h.Handle([]string{"SET", "a", "1"}); !reflect.DeepEqual(reply, resp.OK()) {
		t.Fatalf("SET reply = %#v, want OK", reply)
	}

	steps := []struct {
		args []string
		want resp.Value
	}{
		{[]string{"EXISTS", "a"}, resp.NewInteger(1)},
		{[]string{"EXISTS", "missing"}, resp.NewInteger(0)},
		{[]string{"EXISTS", "a", "missing"}, resp.NewInteger(1)},
		{[]string{"EXISTS", "a", "a", "a"}, resp.NewInteger(3)}, // duplicates counted
		{[]string{"DEL", "a", "a", "a"}, resp.NewInteger(1)},    // ...but DEL can only delete once
		{[]string{"EXISTS", "a"}, resp.NewInteger(0)},
	}
	for _, s := range steps {
		if got := h.Handle(s.args); !reflect.DeepEqual(got, s.want) {
			t.Errorf("Handle(%q) = %#v, want %#v", s.args, got, s.want)
		}
	}
}
