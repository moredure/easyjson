// Package jwriter contains a JSON writer.
package jwriter

import (
	"io"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/moredure/easyjson/buffer"
)

// Flags describe various encoding options. The behavior may be actually implemented in the encoder, but
// Flags field in Writer is used to set and pass them around.
type Flags int

const (
	NilMapAsEmpty   Flags = 1 << iota // Encode nil map as '{}' rather than 'null'.
	NilSliceAsEmpty                   // Encode nil slice as '[]' rather than 'null'.
)

// Writer is a JSON writer.
type Writer struct {
	Flags Flags

	Error        error
	Buffer       buffer.Buffer
	NoEscapeHTML bool
}


const (
	minBitSize = 6 // 2**6=64 is a CPU cache line size
	steps      = 20

	minSize = 1 << minBitSize
	maxSize = 1 << (minBitSize + steps - 1)

	calibrateCallsThreshold = 42000
	maxPercentile           = 0.95
)

// Pool represents byte buffer pool.
//
// Distinct pools may be used for distinct types of byte buffers.
// Properly determined byte buffer types with their own pools may help reducing
// memory waste.
type Pool struct {
	calls       [steps]uint64
	calibrating uint64

	defaultSize uint64
	maxSize     uint64

	pool sync.Pool
}

var defaultPool Pool

// Get returns an empty byte buffer from the pool.
//
// Got byte buffer may be returned to the pool via Put call.
// This reduces the number of memory allocations required for byte buffer
// management.
func Get() *Writer { return defaultPool.Get() }

// Get returns new byte buffer with zero length.
//
// The byte buffer may be returned to the pool via Put after the use
// in order to minimize GC overhead.
func (p *Pool) Get() *Writer {
	v := p.pool.Get()
	if v != nil {
		return v.(*Writer)
	}
	return &Writer{
		Buffer: buffer.Buffer{Buf: make([]byte, 0, atomic.LoadUint64(&p.defaultSize))},
	}
}

// Put returns byte buffer to the pool.
//
// ByteBuffer.B mustn't be touched after returning it to the pool.
// Otherwise data races will occur.
func Put(b *Writer) { defaultPool.Put(b) }

// Put releases byte buffer obtained via Get to the pool.
//
// The buffer mustn't be accessed after returning to the pool.
func (p *Pool) Put(b *Writer) {
	idx := index(len(b.Buffer.Buf))

	if atomic.AddUint64(&p.calls[idx], 1) > calibrateCallsThreshold {
		p.calibrate()
	}

	maxSize := int(atomic.LoadUint64(&p.maxSize))
	if maxSize == 0 || cap(b.Buffer.Buf) <= maxSize {
		b.Reset()
		p.pool.Put(b)
	}
}

func (p *Pool) calibrate() {
	if !atomic.CompareAndSwapUint64(&p.calibrating, 0, 1) {
		return
	}

	a := make(callSizes, 0, steps)
	var callsSum uint64
	for i := uint64(0); i < steps; i++ {
		calls := atomic.SwapUint64(&p.calls[i], 0)
		callsSum += calls
		a = append(a, callSize{
			calls: calls,
			size:  minSize << i,
		})
	}
	sort.Sort(a)

	defaultSize := a[0].size
	maxSize := defaultSize

	maxSum := uint64(float64(callsSum) * maxPercentile)
	callsSum = 0
	for i := 0; i < steps; i++ {
		if callsSum > maxSum {
			break
		}
		callsSum += a[i].calls
		size := a[i].size
		if size > maxSize {
			maxSize = size
		}
	}

	atomic.StoreUint64(&p.defaultSize, defaultSize)
	atomic.StoreUint64(&p.maxSize, maxSize)

	atomic.StoreUint64(&p.calibrating, 0)
}

type callSize struct {
	calls uint64
	size  uint64
}

type callSizes []callSize

func (ci callSizes) Len() int {
	return len(ci)
}

func (ci callSizes) Less(i, j int) bool {
	return ci[i].calls > ci[j].calls
}

func (ci callSizes) Swap(i, j int) {
	ci[i], ci[j] = ci[j], ci[i]
}

func index(n int) int {
	n--
	n >>= minBitSize
	idx := 0
	for n > 0 {
		n >>= 1
		idx++
	}
	if idx >= steps {
		idx = steps - 1
	}
	return idx
}


func (w *Writer) Reset() {
	w.Flags = 0
	w.Error = nil
	w.Buffer.Reset()
	w.NoEscapeHTML = false
}

// Size returns the size of the data that was written out.
func (w *Writer) Size() int {
	return w.Buffer.Size()
}

// DumpTo outputs the data to given io.Writer, resetting the buffer.
func (w *Writer) DumpTo(out io.Writer) (written int, err error) {
	return w.Buffer.DumpTo(out)
}

// BuildBytes returns writer data as a single byte slice. You can optionally provide one byte slice
// as argument that it will try to reuse.
func (w *Writer) BuildBytes(reuse ...[]byte) ([]byte, error) {
	if w.Error != nil {
		return nil, w.Error
	}

	return w.Buffer.BuildBytes(reuse...), nil
}

// ReadCloser returns an io.ReadCloser that can be used to read the data.
// ReadCloser also resets the buffer.
func (w *Writer) ReadCloser() (io.ReadCloser, error) {
	if w.Error != nil {
		return nil, w.Error
	}

	return w.Buffer.ReadCloser(), nil
}

// RawByte appends raw binary data to the buffer.
func (w *Writer) RawByte(c byte) {
	w.Buffer.AppendByte(c)
}

// RawByte appends raw binary data to the buffer.
func (w *Writer) RawString(s string) {
	w.Buffer.AppendString(s)
}

// Raw appends raw binary data to the buffer or sets the error if it is given. Useful for
// calling with results of MarshalJSON-like functions.
func (w *Writer) Raw(data []byte, err error) {
	switch {
	case w.Error != nil:
		return
	case err != nil:
		w.Error = err
	case len(data) > 0:
		w.Buffer.AppendBytes(data)
	default:
		w.RawString("null")
	}
}

// RawText encloses raw binary data in quotes and appends in to the buffer.
// Useful for calling with results of MarshalText-like functions.
func (w *Writer) RawText(data []byte, err error) {
	switch {
	case w.Error != nil:
		return
	case err != nil:
		w.Error = err
	case len(data) > 0:
		w.String(string(data))
	default:
		w.RawString("null")
	}
}

// Base64Bytes appends data to the buffer after base64 encoding it
func (w *Writer) Base64Bytes(data []byte) {
	if data == nil {
		w.Buffer.AppendString("null")
		return
	}
	w.Buffer.AppendByte('"')
	w.base64(data)
	w.Buffer.AppendByte('"')
}

func (w *Writer) Uint8(n uint8) {
	w.Buffer.Buf = strconv.AppendUint(w.Buffer.Buf, uint64(n), 10)
}

func (w *Writer) Uint16(n uint16) {
	w.Buffer.Buf = strconv.AppendUint(w.Buffer.Buf, uint64(n), 10)
}

func (w *Writer) Uint32(n uint32) {
	w.Buffer.Buf = strconv.AppendUint(w.Buffer.Buf, uint64(n), 10)
}

func (w *Writer) Uint(n uint) {
	w.Buffer.Buf = strconv.AppendUint(w.Buffer.Buf, uint64(n), 10)
}

func (w *Writer) Uint64(n uint64) {
	w.Buffer.Buf = strconv.AppendUint(w.Buffer.Buf, n, 10)
}

func (w *Writer) Int8(n int8) {
	w.Buffer.Buf = strconv.AppendInt(w.Buffer.Buf, int64(n), 10)
}

func (w *Writer) Int16(n int16) {
	w.Buffer.Buf = strconv.AppendInt(w.Buffer.Buf, int64(n), 10)
}

func (w *Writer) Int32(n int32) {
	w.Buffer.Buf = strconv.AppendInt(w.Buffer.Buf, int64(n), 10)
}

func (w *Writer) Int(n int) {
	w.Buffer.Buf = strconv.AppendInt(w.Buffer.Buf, int64(n), 10)
}

func (w *Writer) Int64(n int64) {
	w.Buffer.Buf = strconv.AppendInt(w.Buffer.Buf, n, 10)
}

func (w *Writer) Uint8Str(n uint8) {
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
	w.Buffer.Buf = strconv.AppendUint(w.Buffer.Buf, uint64(n), 10)
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
}

func (w *Writer) Uint16Str(n uint16) {
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
	w.Buffer.Buf = strconv.AppendUint(w.Buffer.Buf, uint64(n), 10)
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
}

func (w *Writer) Uint32Str(n uint32) {
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
	w.Buffer.Buf = strconv.AppendUint(w.Buffer.Buf, uint64(n), 10)
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
}

func (w *Writer) UintStr(n uint) {
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
	w.Buffer.Buf = strconv.AppendUint(w.Buffer.Buf, uint64(n), 10)
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
}

func (w *Writer) Uint64Str(n uint64) {
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
	w.Buffer.Buf = strconv.AppendUint(w.Buffer.Buf, n, 10)
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
}

func (w *Writer) UintptrStr(n uintptr) {
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
	w.Buffer.Buf = strconv.AppendUint(w.Buffer.Buf, uint64(n), 10)
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
}

func (w *Writer) Int8Str(n int8) {
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
	w.Buffer.Buf = strconv.AppendInt(w.Buffer.Buf, int64(n), 10)
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
}

func (w *Writer) Int16Str(n int16) {
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
	w.Buffer.Buf = strconv.AppendInt(w.Buffer.Buf, int64(n), 10)
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
}

func (w *Writer) Int32Str(n int32) {
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
	w.Buffer.Buf = strconv.AppendInt(w.Buffer.Buf, int64(n), 10)
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
}

func (w *Writer) IntStr(n int) {
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
	w.Buffer.Buf = strconv.AppendInt(w.Buffer.Buf, int64(n), 10)
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
}

func (w *Writer) Int64Str(n int64) {
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
	w.Buffer.Buf = strconv.AppendInt(w.Buffer.Buf, n, 10)
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
}

func (w *Writer) Float32(n float32) {
	w.Buffer.Buf = strconv.AppendFloat(w.Buffer.Buf, float64(n), 'g', -1, 32)
}

func (w *Writer) Float32Str(n float32) {
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
	w.Buffer.Buf = strconv.AppendFloat(w.Buffer.Buf, float64(n), 'g', -1, 32)
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
}

func (w *Writer) Float64(n float64) {
	w.Buffer.Buf = strconv.AppendFloat(w.Buffer.Buf, n, 'g', -1, 64)
}

func (w *Writer) Float64Str(n float64) {
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
	w.Buffer.Buf = strconv.AppendFloat(w.Buffer.Buf, float64(n), 'g', -1, 64)
	w.Buffer.Buf = append(w.Buffer.Buf, '"')
}

func (w *Writer) Bool(v bool) {
	if v {
		w.Buffer.Buf = append(w.Buffer.Buf, "true"...)
	} else {
		w.Buffer.Buf = append(w.Buffer.Buf, "false"...)
	}
}

const chars = "0123456789abcdef"

func getTable(falseValues ...int) [128]bool {
	table := [128]bool{}

	for i := 0; i < 128; i++ {
		table[i] = true
	}

	for _, v := range falseValues {
		table[v] = false
	}

	return table
}

var (
	htmlEscapeTable   = getTable(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, '"', '&', '<', '>', '\\')
	htmlNoEscapeTable = getTable(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, '"', '\\')
)

func (w *Writer) String(s string) {
	w.Buffer.AppendByte('"')

	// Portions of the string that contain no escapes are appended as
	// byte slices.

	p := 0 // last non-escape symbol

	escapeTable := &htmlEscapeTable
	if w.NoEscapeHTML {
		escapeTable = &htmlNoEscapeTable
	}

	for i := 0; i < len(s); {
		c := s[i]

		if c < utf8.RuneSelf {
			if escapeTable[c] {
				// single-width character, no escaping is required
				i++
				continue
			}

			w.Buffer.AppendString(s[p:i])
			switch c {
			case '\t':
				w.Buffer.AppendString(`\t`)
			case '\r':
				w.Buffer.AppendString(`\r`)
			case '\n':
				w.Buffer.AppendString(`\n`)
			case '\\':
				w.Buffer.AppendString(`\\`)
			case '"':
				w.Buffer.AppendString(`\"`)
			default:
				w.Buffer.AppendString(`\u00`)
				w.Buffer.AppendByte(chars[c>>4])
				w.Buffer.AppendByte(chars[c&0xf])
			}

			i++
			p = i
			continue
		}

		// broken utf
		runeValue, runeWidth := utf8.DecodeRuneInString(s[i:])
		if runeValue == utf8.RuneError && runeWidth == 1 {
			w.Buffer.AppendString(s[p:i])
			w.Buffer.AppendString(`\ufffd`)
			i++
			p = i
			continue
		}

		// jsonp stuff - tab separator and line separator
		if runeValue == '\u2028' || runeValue == '\u2029' {
			w.Buffer.AppendString(s[p:i])
			w.Buffer.AppendString(`\u202`)
			w.Buffer.AppendByte(chars[runeValue&0xf])
			i += runeWidth
			p = i
			continue
		}
		i += runeWidth
	}
	w.Buffer.AppendString(s[p:])
	w.Buffer.AppendByte('"')
}

const encode = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
const padChar = '='

func (w *Writer) base64(in []byte) {

	if len(in) == 0 {
		return
	}

	si := 0
	n := (len(in) / 3) * 3

	for si < n {
		// Convert 3x 8bit source bytes into 4 bytes
		val := uint(in[si+0])<<16 | uint(in[si+1])<<8 | uint(in[si+2])

		w.Buffer.Buf = append(w.Buffer.Buf, encode[val>>18&0x3F], encode[val>>12&0x3F], encode[val>>6&0x3F], encode[val&0x3F])

		si += 3
	}

	remain := len(in) - si
	if remain == 0 {
		return
	}

	// Add the remaining small block
	val := uint(in[si+0]) << 16
	if remain == 2 {
		val |= uint(in[si+1]) << 8
	}

	w.Buffer.Buf = append(w.Buffer.Buf, encode[val>>18&0x3F], encode[val>>12&0x3F])

	switch remain {
	case 2:
		w.Buffer.Buf = append(w.Buffer.Buf, encode[val>>6&0x3F], byte(padChar))
	case 1:
		w.Buffer.Buf = append(w.Buffer.Buf, byte(padChar), byte(padChar))
	}
}
