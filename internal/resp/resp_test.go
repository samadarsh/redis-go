package resp

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestRead(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  Value
	}{
		{"simple string", "+OK\r\n", NewSimpleString("OK")},
		{"error", "-ERR boom\r\n", NewError("ERR boom")},
		{"integer", ":42\r\n", NewInteger(42)},
		{"negative integer", ":-7\r\n", NewInteger(-7)},
		{"bulk string", "$5\r\nhello\r\n", NewBulkString("hello")},
		{"empty bulk string", "$0\r\n\r\n", NewBulkString("")},
		{"binary-safe bulk string", "$4\r\na\r\nb\r\n", NewBulkString("a\r\nb")},
		{"null bulk string", "$-1\r\n", NullBulkString()},
		{"null array", "*-1\r\n", Value{Type: Array, Null: true}},
		{"empty array", "*0\r\n", NewArray()},
		{
			"command array",
			"*3\r\n$3\r\nSET\r\n$4\r\nname\r\n$5\r\nredis\r\n",
			NewArray(NewBulkString("SET"), NewBulkString("name"), NewBulkString("redis")),
		},
		{
			"nested array",
			"*2\r\n:1\r\n*1\r\n+x\r\n",
			NewArray(NewInteger(1), NewArray(NewSimpleString("x"))),
		},
		{"inline command", "PING\r\n", NewArray(NewBulkString("PING"))},
		{
			"inline with LF only and extra spaces",
			"  ECHO   hi \n",
			NewArray(NewBulkString("ECHO"), NewBulkString("hi")),
		},
		{"blank inline line", "\r\n", NewArray()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewReader(strings.NewReader(tt.input)).Read()
			if err != nil {
				t.Fatalf("Read(%q) error: %v", tt.input, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Read(%q)\n got  %#v\n want %#v", tt.input, got, tt.want)
			}
		})
	}
}

func TestReadErrors(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"bad integer", ":12x\r\n", ErrProtocol},
		{"bad bulk length", "$abc\r\n", ErrProtocol},
		{"negative bulk length", "$-2\r\n", ErrProtocol},
		{"bulk missing CRLF", "$2\r\nabcd", ErrProtocol},
		{"negative array length", "*-2\r\n", ErrProtocol},
		{"truncated bulk", "$5\r\nhel", io.ErrUnexpectedEOF},
		{"truncated array", "*2\r\n$1\r\na\r\n", io.ErrUnexpectedEOF},
		{"truncated line", "+OK", io.ErrUnexpectedEOF},
		{"line too long", "+" + strings.Repeat("a", maxLineLen+1) + "\r\n", ErrProtocol},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewReader(strings.NewReader(tt.input)).Read()
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Read(%.20q) error = %v, want %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestReadCleanEOF(t *testing.T) {
	_, err := NewReader(strings.NewReader("")).Read()
	if err != io.EOF {
		t.Fatalf("got %v, want io.EOF", err)
	}
}

func TestReadMultipleValues(t *testing.T) {
	r := NewReader(strings.NewReader("+a\r\n:1\r\n$1\r\nb\r\n"))
	want := []Value{NewSimpleString("a"), NewInteger(1), NewBulkString("b")}
	for i, w := range want {
		got, err := r.Read()
		if err != nil {
			t.Fatalf("value %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, w) {
			t.Errorf("value %d: got %#v, want %#v", i, got, w)
		}
	}
	if _, err := r.Read(); err != io.EOF {
		t.Errorf("after last value: got %v, want io.EOF", err)
	}
}

func TestMarshal(t *testing.T) {
	tests := []struct {
		v    Value
		want string
	}{
		{OK(), "+OK\r\n"},
		{NewError("ERR bad\r\nthing"), "-ERR bad  thing\r\n"},
		{NewInteger(-3), ":-3\r\n"},
		{NewBulkString("hi"), "$2\r\nhi\r\n"},
		{NullBulkString(), "$-1\r\n"},
		{NewArray(), "*0\r\n"},
		{NewArray(NewBulkString("a"), NewInteger(1)), "*2\r\n$1\r\na\r\n:1\r\n"},
	}
	for _, tt := range tests {
		if got := string(tt.v.Marshal()); got != tt.want {
			t.Errorf("Marshal(%#v) = %q, want %q", tt.v, got, tt.want)
		}
	}
}

// Anything we write, we must be able to read back unchanged.
func TestRoundTrip(t *testing.T) {
	values := []Value{
		OK(),
		NewInteger(1 << 40),
		NewBulkString("binary\x00\r\ndata"),
		NullBulkString(),
		NewArray(NewBulkString("SET"), NewBulkString("k"), NewArray(NewInteger(1))),
	}

	var buf bytes.Buffer
	w := NewWriter(&buf)
	for _, v := range values {
		if err := w.Write(v); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	r := NewReader(&buf)
	for _, want := range values {
		got, err := r.Read()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("round trip: got %#v, want %#v", got, want)
		}
	}
}
