// Package resp implements RESP2 (REdis Serialization Protocol), the wire
// format Redis clients and servers use to talk to each other over TCP.
//
// Every RESP value starts with a one-byte type marker and ends with "\r\n":
//
//	+OK\r\n                          simple string
//	-ERR something broke\r\n         error
//	:42\r\n                          integer
//	$5\r\nhello\r\n                  bulk string ($-1\r\n means null)
//	*2\r\n$3\r\nGET\r\n$1\r\nk\r\n   array of 2 bulk strings
//
// Clients send commands as arrays of bulk strings; servers reply with
// any of the types above.
//
// Spec: https://redis.io/docs/latest/develop/reference/protocol-spec/
package resp

import "strconv"

// Type is the first byte of every RESP value. It tells the reader how to
// parse the bytes that follow.
type Type byte

const (
	SimpleString Type = '+'
	Error        Type = '-'
	Integer      Type = ':'
	BulkString   Type = '$'
	Array        Type = '*'
)

// Value is a single RESP value. Which fields are meaningful depends on Type.
type Value struct {
	Type  Type
	Str   string  // SimpleString, Error, BulkString
	Int   int64   // Integer
	Array []Value // Array
	Null  bool    // null BulkString ($-1) or null Array (*-1)
}

// Helpers for building replies.

func NewSimpleString(s string) Value { return Value{Type: SimpleString, Str: s} }
func NewError(msg string) Value      { return Value{Type: Error, Str: msg} }
func NewInteger(n int64) Value       { return Value{Type: Integer, Int: n} }
func NewBulkString(s string) Value   { return Value{Type: BulkString, Str: s} }
func NullBulkString() Value          { return Value{Type: BulkString, Null: true} }
func NewArray(vals ...Value) Value {
	if vals == nil {
		vals = []Value{} // an empty array is not the same as a null one
	}
	return Value{Type: Array, Array: vals}
}

// OK is the reply Redis sends for successful writes like SET.
func OK() Value { return NewSimpleString("OK") }

const crlf = "\r\n"

// Marshal encodes v into RESP bytes.
func (v Value) Marshal() []byte { return v.AppendTo(nil) }

// AppendTo appends the RESP encoding of v to b and returns the extended
// slice. Reusing b between calls avoids an allocation per reply.
func (v Value) AppendTo(b []byte) []byte {
	b = append(b, byte(v.Type))
	switch v.Type {
	case SimpleString, Error:
		// These types are line-based, so a CR or LF inside would corrupt
		// the stream. Replace them with spaces like Redis does.
		for i := 0; i < len(v.Str); i++ {
			c := v.Str[i]
			if c == '\r' || c == '\n' {
				c = ' '
			}
			b = append(b, c)
		}
	case Integer:
		b = strconv.AppendInt(b, v.Int, 10)
	case BulkString:
		if v.Null {
			return append(b, "-1\r\n"...)
		}
		// Bulk strings are length-prefixed, so they can hold any bytes.
		b = strconv.AppendInt(b, int64(len(v.Str)), 10)
		b = append(b, crlf...)
		b = append(b, v.Str...)
	case Array:
		if v.Null {
			return append(b, "-1\r\n"...)
		}
		b = strconv.AppendInt(b, int64(len(v.Array)), 10)
		b = append(b, crlf...)
		for _, elem := range v.Array {
			b = elem.AppendTo(b)
		}
		return b // each element already ended with its own CRLF
	}
	return append(b, crlf...)
}
