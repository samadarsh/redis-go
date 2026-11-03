package resp

import (
	"bufio"
	"io"
)

// Writer encodes RESP values onto a stream. Output is buffered: call Flush
// to actually send what has been written.
type Writer struct {
	wr  *bufio.Writer
	buf []byte
}

func NewWriter(w io.Writer) *Writer {
	return &Writer{wr: bufio.NewWriter(w)}
}

// Write encodes v into the buffer.
func (w *Writer) Write(v Value) error {
	w.buf = v.AppendTo(w.buf[:0])
	_, err := w.wr.Write(w.buf)
	return err
}

// Flush sends all buffered output.
func (w *Writer) Flush() error { return w.wr.Flush() }
