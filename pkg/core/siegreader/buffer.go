// Copyright 2014 Richard Lehane. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Scenarios:
// a) Stream - just copy into a big buffer as at present (... but if there is a MaxBof??)
// b) File
//    b i)   Satisifed with small read beginning
//    b ii)  Small enough for full read
//    b iii) Mmap
//    b iv) Too big for MMap - small buffers and expose ReaderAt

// Package siegreader implements multiple independent Readers (and ReverseReaders) from a single Buffer.
//
// Example:
//   buf := siegreader.New()
//	 err := buf.SetSource(io.Reader)
//   if err != nil {
//     log.Fatal(err)
//   }
//   rdr := buf.Reader()
//	 second_rdr := buf.Reader()
//   reverse_rdr, err := buf.ReverseReader()
//   if err != nil {
//	   log.Fatal(err)
//   }
//   i, _ := rdr.Read(slc)
//   i2, _ := second_rdr.Read(slc2)
//   i3, _ := reverse_rdr.Read(slc3)
package siegreader

import (
	"errors"
	"io"
	"log"
	"os"
	"sync"
)

type Buf interface {
	SetQuit(chan struct{})
	SetSource(io.Reader) error
	Size() int64
	SizeNow() int64
	Slice(off int64, length int, whence bool) ([]byte, error)
	canSeek(off int64, whence bool) (bool, error)
}

var (
	ErrQuit      = errors.New("siegreader: quit chan closed while awaiting EOF")
	ErrNilBuffer = errors.New("siegreader: attempt to SetSource on a nil buffer")
)

const (
	readSz      = 4096
	initialRead = readSz * 2
)

type protected struct {
	sync.Mutex
	val     int
	eofRead bool
}

// Buffer wraps an io.Reader, buffering its contents in byte slices that will keep growing until IO.EOF.
// It supports multiple concurrent Readers, including Readers reading from the end of the stream (ReverseReaders)
type Buffer struct {
	quit      chan struct{} // allows quittting - otherwise will block forever while awaiting EOF
	drain     bool          // Does this Buffer have a regular reader that we can expect will read to the EOF (and hence allow ReverseReaders to wait)
	src       io.Reader
	buf, eof  []byte
	eofc      chan struct{} // signals if EOF bytes are available. When EOF bytes are available, this chan is closed
	completec chan struct{} // signals when the file has been completely read, allows EOF scanning beyond the small buffer
	complete  bool          // marks that the file has been completely read
	sz        int64
	w         protected // index of latest write
}

// New instatatiates a new Buffer with a buf size of 4096*3, and an end-of-file buf size of 4096
func New() *Buffer {
	b := new(Buffer)
	b.buf, b.eof = make([]byte, initialRead), make([]byte, readSz)
	return b
}

func (b *Buffer) reset() {
	b.eofc, b.completec = make(chan struct{}), make(chan struct{})
	b.complete = false
	b.sz = 0
	b.w.Lock()
	b.w.val = 0
	b.w.eofRead = false
	b.w.Unlock()
}

// SetSource sets the buffer's source.
// Can be any io.Reader. If it is an os.File, will load EOF buffer early. Otherwise waits for a complete read.
// The source can be reset to recycle an existing Buffer.
// Siegreader blocks on EOF reads or Size() calls when the reader isn't a file or the stream isn't completely read. The quit channel overrides this block.
func (b *Buffer) SetSource(r io.Reader) error {
	if b == nil {
		return errors.New("Siegreader: attempt to SetSource on a nil buffer")
	}
	b.reset()
	b.src = r
	file, ok := r.(*os.File)
	if ok {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		b.sz = info.Size()
		if b.sz > int64(initialRead) {
			b.eof = b.eof[:cap(b.eof)]
		} else {
			b.eof = b.eof[:0]
		}
	} else {
		b.sz = 0
		b.eof = b.eof[:0]
	}
	_, err := b.fill() // initial fill
	return err
}

func (b *Buffer) SetQuit(q chan struct{}) {
	b.quit = q
}

// Size returns the buffer's size, which is available immediately for files. Must wait for full read for streams.
func (b *Buffer) Size() int {
	if b.sz > 0 {
		return int(b.sz)
	}
	select {
	case <-b.eofc:
		return int(b.sz)
	case <-b.quit:
		return 0
	}
}

// non-blocking Size(), for use with zip reader
func (b *Buffer) SizeNow() int64 {
	if b.sz > 0 {
		return b.sz
	}
	b.w.Lock()
	defer b.w.Unlock()
	var err error
	for _, err = b.fill(); err == nil; _, err = b.fill() {
	}
	if err != io.EOF {
		log.Printf("SIEGREADER WARNING: FAILED TO READ FULL STREAM, ERROR MESSAGE %v", err)
		return 0
	}
	return b.sz
}

func (b *Buffer) grow() {
	// Rules for growing:
	// - if we need to grow, we have passed the initial read and can assume we will need whole file so, if we have a sz grow to it straight away
	// - otherwise, double capacity each time
	var buf []byte
	if b.sz > 0 {
		buf = make([]byte, int(b.sz))
	} else {
		buf = make([]byte, cap(b.buf)*2)
	}
	copy(buf, b.buf[:b.w.val]) // don't care about unlocking as grow() is only called by fill()
	b.buf = buf
}

// Rules for filling:
// - if we have a sz greater than 0, if there is stuff in the eof buffer, and if we are less than readSz from the end, copy across from the eof buffer
// - read readsz * 2 at a time
func (b *Buffer) fill() (int, error) {
	// if we've run out of room, grow the buffer
	if len(b.buf)-readSz < b.w.val {
		b.grow()
	}
	// if we have an eof buffer, and we are near the end of the file, avoid an extra read by copying straight into the main buffer
	if len(b.eof) > 0 && b.w.eofRead && b.w.val+readSz >= int(b.sz) {
		close(b.completec)
		b.complete = true
		lr := int(b.sz) - b.w.val
		b.w.val += copy(b.buf[b.w.val:b.w.val+lr], b.eof[readSz-lr:])
		return b.w.val, io.EOF
	}
	// otherwise, let's read
	e := b.w.val + readSz
	if e > len(b.buf) {
		e = len(b.buf)
	}
	i, err := b.src.Read(b.buf[b.w.val:e])
	if i < readSz {
		err = io.EOF // Readers can give EOF or nil here
	}
	if err != nil {
		close(b.completec)
		b.complete = true
		if err == io.EOF {
			b.w.val += i
			// if we haven't got an eof buf already
			if len(b.eof) < readSz {
				b.sz = int64(b.w.val)
				close(b.eofc)
			}
		}
		return b.w.val, err
	}
	b.w.val += i
	return b.w.val, nil
}

func (b *Buffer) fillEof() error {
	// return nil for a non-file or small file reader
	if len(b.eof) < readSz {
		return nil
	}
	b.w.Lock()
	defer b.w.Unlock()
	if b.w.eofRead {
		return nil // another reverse reader has already filled the buffer
	}
	rs := b.src.(io.ReadSeeker)
	_, err := rs.Seek(0-int64(readSz), 2)
	if err != nil {
		return err
	}
	_, err = rs.Read(b.eof)
	if err != nil {
		return err
	}
	_, err = rs.Seek(int64(b.w.val), 0)
	if err != nil {
		return err
	}
	close(b.eofc)
	b.w.eofRead = true
	return nil
}

// Return a slice from the buffer that begins at offset s and has length l
func (b *Buffer) Slice(s, l int) ([]byte, error) {
	b.w.Lock()
	defer b.w.Unlock()
	var err error
	var bound int
	if s+l > b.w.val && !b.complete {
		for bound, err = b.fill(); s+l > bound && err == nil; bound, err = b.fill() {
		}
	}
	if err == nil && !b.complete {
		return b.buf[s : s+l], nil
	}
	if err == io.EOF || b.complete {
		if s+l > b.w.val {
			if s > b.w.val {
				return nil, io.EOF
			}
			// in the case of an empty file
			if b.Size() == 0 {
				return nil, io.EOF
			}
			return b.buf[s:b.w.val], io.EOF
		} else {
			return b.buf[s : s+l], nil
		}
	}
	return nil, err
}

// Return a slice from the end of the buffer that begins at offset s and has length l.
// This will block until the slice is available (which may be until the full stream is read).
func (b *Buffer) EofSlice(s, l int) ([]byte, error) {
	// block until the EOF is available or we quit
	select {
	case <-b.quit:
		return nil, ErrQuit
	case <-b.eofc:
	}
	var buf []byte
	if len(b.eof) > 0 && s+l <= len(b.eof) {
		buf = b.eof
	} else {
		select {
		case <-b.quit:
			return nil, ErrQuit
		case <-b.completec:
		}
		buf = b.buf[:int(b.sz)]
		if s+l == len(buf) {
			return buf[:len(buf)-s], io.EOF
		}
	}
	if s+l > len(buf) {
		if s > len(buf) {
			return nil, io.EOF
		}
		return buf[:len(buf)-s], io.EOF
	}
	return buf[len(buf)-(s+l) : len(buf)-s], nil
}

// SafeSlice calls Slice or EofSlice (which one depends on the rev argument: true for EofSlice)
func (b *Buffer) SafeSlice(s, l int, rev bool) ([]byte, error) {
	if rev {
		return b.EofSlice(s, l)
	} else {
		return b.Slice(s, l)
	}
}

// MustSlice calls Slice or EofSlice (which one depends on the rev argument: true for EofSlice) and suppresses the error.
// If a non io.EOF error is encountered, it will be logged as a warning.
func (b *Buffer) MustSlice(s, l int, rev bool) []byte {
	var slc []byte
	var err error
	if rev {
		slc, err = b.EofSlice(s, l)
	} else {
		slc, err = b.Slice(s, l)
	}
	if err != nil && err != io.EOF {
		log.Printf("Siegreader warning: failed to slice %d for length %d; slice length is is %d; reverse is %b", s, l, len(slc), rev)
	}
	return slc
}

// fill until a seek to a particular offset is possible, then return true, if it is impossible return false
func (b *Buffer) canSeek(o int64, rev bool) (bool, error) {
	if rev {
		if b.sz > 0 {
			o = b.sz - o
			if o < 0 {
				return false, nil
			}
			// continue on to fill below
		} else {
			var err error
			for _, err = b.fill(); err == nil; _, err = b.fill() {
			}
			if err != io.EOF {
				return false, err
			}
			if b.sz-o < 0 {
				return false, nil
			}
			return true, nil
		}
	}
	b.w.Lock()
	defer b.w.Unlock()
	var err error
	var bound int
	if o > int64(b.w.val) {
		for bound, err = b.fill(); o > int64(bound) && err == nil; bound, err = b.fill() {
		}
	}
	if err == nil {
		return true, nil
	}
	if err == io.EOF {
		if o > int64(b.w.val) {
			return false, err
		}
		return true, nil
	}
	return false, err
}
