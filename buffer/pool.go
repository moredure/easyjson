// Package buffer implements a buffer for serialization, consisting of a chain of []byte-s to
// reduce copying and to allow reuse of individual chunks.
package buffer

import (
	"bytes"
	"io"
)

// Buffer is a buffer optimized for serialization without extra copying.
type Buffer struct {

	// Buf is a byte buffer to use in append-like workloads.
	// See example code for details.
	Buf []byte
}

// Len returns the size of the byte buffer.
func (b *Buffer) Len() int {
	return len(b.Buf)
}

// ReadFrom implements io.ReaderFrom.
//
// The function appends all the data read from r to b.
func (b *Buffer) ReadFrom(r io.Reader) (int64, error) {
	p := b.Buf
	nStart := int64(len(p))
	nMax := int64(cap(p))
	n := nStart
	if nMax == 0 {
		nMax = 64
		p = make([]byte, nMax)
	} else {
		p = p[:nMax]
	}
	for {
		if n == nMax {
			nMax *= 2
			bNew := make([]byte, nMax)
			copy(bNew, p)
			p = bNew
		}
		nn, err := r.Read(p[n:])
		n += int64(nn)
		if err != nil {
			b.Buf = p[:n]
			n -= nStart
			if err == io.EOF {
				return n, nil
			}
			return n, err
		}
	}
}

// WriteTo implements io.WriterTo.
func (b *Buffer) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(b.Buf)
	return int64(n), err
}

// Bytes returns b.Buf, i.e. all the bytes accumulated in the buffer.
//
// The purpose of this function is bytes.Buffer compatibility.
func (b *Buffer) Bytes() []byte {
	return b.Buf
}

// Write implements io.Writer - it appends p to ByteBuffer.Buf
func (b *Buffer) Write(p []byte) (int, error) {
	b.Buf = append(b.Buf, p...)
	return len(p), nil
}

// WriteByte appends the byte c to the buffer.
//
// The purpose of this function is bytes.Buffer compatibility.
//
// The function always returns nil.
func (b *Buffer) WriteByte(c byte) error {
	b.Buf = append(b.Buf, c)
	return nil
}

// WriteString appends s to ByteBuffer.Buf.
func (b *Buffer) WriteString(s string) (int, error) {
	b.Buf = append(b.Buf, s...)
	return len(s), nil
}

// Set sets ByteBuffer.Buf to p.
func (b *Buffer) Set(p []byte) {
	b.Buf = append(b.Buf[:0], p...)
}

// SetString sets ByteBuffer.Buf to s.
func (b *Buffer) SetString(s string) {
	b.Buf = append(b.Buf[:0], s...)
}

// String returns string representation of ByteBuffer.Buf.
func (b *Buffer) String() string {
	return string(b.Buf)
}

// Reset makes ByteBuffer.Buf empty.
func (b *Buffer) Reset() {
	b.Buf = b.Buf[:0]
}

// AppendByte appends a single byte to buffer.
func (b *Buffer) AppendByte(data byte) {
	b.Buf = append(b.Buf, data)
}

// AppendBytes appends a byte slice to buffer.
func (b *Buffer) AppendBytes(data []byte) {
	b.Buf = append(b.Buf, data...) // fast path
}

// AppendString appends a string to buffer.
func (b *Buffer) AppendString(data string) {
	b.Buf = append(b.Buf, data...) // fast path
}

// Size computes the size of a buffer by adding sizes of every chunk.
func (b *Buffer) Size() int {
	return len(b.Buf)
}

// DumpTo outputs the contents of a buffer to a writer and resets the buffer.
func (b *Buffer) DumpTo(w io.Writer) (written int, err error) {
	return w.Write(b.Buf)
}

// BuildBytes creates a single byte slice with all the contents of the buffer. Data is
// copied if it does not fit in a single chunk. You can optionally provide one byte
// slice as argument that it will try to reuse.
func (b *Buffer) BuildBytes(reuse ...[]byte) []byte {
	return bytes.Repeat(b.Buf, 1)
}

type readCloser struct {
	offset int
	bufs   [][]byte
}

func (r *readCloser) Read(p []byte) (n int, err error) {
	for _, buf := range r.bufs {
		// Copy as much as we can.
		x := copy(p[n:], buf[r.offset:])
		n += x // Increment how much we filled.

		// Did we empty the whole buffer?
		if r.offset+x == len(buf) {
			// On to the next buffer.
			r.offset = 0
			r.bufs = r.bufs[1:]

		} else {
			r.offset += x
		}

		if n == len(p) {
			break
		}
	}
	// No buffers left or nothing read?
	if len(r.bufs) == 0 {
		err = io.EOF
	}
	return
}

func (r *readCloser) Close() error {
	r.bufs = nil
	return nil
}

// ReadCloser creates an io.ReadCloser with all the contents of the buffer.
func (b *Buffer) ReadCloser() io.ReadCloser {
	return &readCloser{0, [][]byte{b.Buf}}
}
