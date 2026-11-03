package resp

import "testing"

// loopReader returns the same bytes forever, so a benchmark can read as
// many commands as it needs without building a giant input first.
type loopReader struct {
	data []byte
	pos  int
}

func (l *loopReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		c := copy(p[n:], l.data[l.pos:])
		n += c
		l.pos = (l.pos + c) % len(l.data)
	}
	return n, nil
}

var setCommand = []byte("*3\r\n$3\r\nSET\r\n$4\r\nname\r\n$5\r\nredis\r\n")

// How long it takes to parse one "SET name redis" command off the wire,
// and how many allocations that costs.
func BenchmarkReadCommand(b *testing.B) {
	r := NewReader(&loopReader{data: setCommand})
	b.ReportAllocs()
	b.SetBytes(int64(len(setCommand)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := r.Read(); err != nil {
			b.Fatal(err)
		}
	}
}

// Encoding a reply. Marshal allocates a new buffer every time; AppendTo
// reuses one, which is what resp.Writer does.
func BenchmarkMarshal(b *testing.B) {
	v := NewArray(NewBulkString("SET"), NewBulkString("name"), NewBulkString("redis"))

	b.Run("Marshal", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = v.Marshal()
		}
	})
	b.Run("AppendTo-reused-buffer", func(b *testing.B) {
		b.ReportAllocs()
		var buf []byte
		for i := 0; i < b.N; i++ {
			buf = v.AppendTo(buf[:0])
		}
	})
}
