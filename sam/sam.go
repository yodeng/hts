// Copyright ©2012 The bíogo.bam Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package sam implements SAM file format reading and writing. The SAM format
// is described in the SAM specification.
//
// http://samtools.github.io/hts-specs/SAMv1.pdf
package sam

import (
	"bufio"
	"errors"
	"io"
)

type Reader struct {
	r *bufio.Reader
	h *Header

	seenRefs map[string]*Reference
}

func NewReader(r io.Reader) (*Reader, error) {
	h, _ := NewHeader(nil, nil)
	sr := &Reader{
		r: bufio.NewReader(r),
		h: h,
	}

	var b []byte
	p, err := sr.r.Peek(1)
	if err != nil {
		return nil, err
	}
	if p[0] != '@' {
		sr.seenRefs = make(map[string]*Reference)
		return sr, nil
	}

	for {
		l, err := sr.r.ReadBytes('\n')
		if err != nil {
			return nil, io.ErrUnexpectedEOF
		}
		b = append(b, l...)
		p, err := sr.r.Peek(1)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if p[0] != '@' {
			break
		}
	}

	err = sr.h.UnmarshalText(b)
	if err != nil {
		return nil, err
	}

	return sr, nil
}

func (r *Reader) Header() *Header {
	return r.h
}

func (r *Reader) Read() (*Record, error) {
	b, err := r.r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	b = b[:len(b)-1]
	if b[len(b)-1] == '\r' {
		b = b[:len(b)-1]
	}
	var rec Record

	// Handle cases where a header was present.
	if r.seenRefs == nil {
		err = rec.UnmarshalSAM(r.h, b)
		if err != nil {
			return nil, err
		}
		return &rec, nil
	}

	// Handle cases where no SAM header is present.
	err = rec.UnmarshalSAM(nil, b)
	if err != nil {
		return nil, err
	}

	if ref, ok := r.seenRefs[rec.Ref.Name()]; ok {
		rec.Ref = ref
	} else if rec.Ref != nil {
		err = r.h.AddReference(rec.Ref)
		if err != nil {
			return nil, err
		}
		r.seenRefs[rec.Ref.Name()] = rec.Ref
	} else {
		r.seenRefs["*"] = nil
	}
	if ref, ok := r.seenRefs[rec.MateRef.Name()]; ok {
		rec.MateRef = ref
	} else if rec.MateRef != nil {
		err = r.h.AddReference(rec.MateRef)
		if err != nil {
			return nil, err
		}
		r.seenRefs[rec.MateRef.Name()] = rec.MateRef
	} else {
		r.seenRefs["*"] = nil
	}

	return &rec, nil
}

// RecordReader wraps types that can read SAM Records.
type RecordReader interface {
	Read() (*Record, error)
}

// Iterator wraps a Reader to provide a convenient loop interface for reading SAM/BAM data.
// Successive calls to the Next method will step through the features of the provided
// Reader. Iteration stops unrecoverably at EOF or the first error.
type Iterator struct {
	r   RecordReader
	rec *Record
	err error
}

// NewIterator returns a Iterator to read from r.
//
//  i, err := NewIterator(r)
//  if err != nil {
//  	return err
//  }
//  for i.Next() {
//  	fn(i.Record())
//  }
//  return i.Error()
//
func NewIterator(r RecordReader) *Iterator { return &Iterator{r: r} }

// Next advances the Iterator past the next record, which will then be available through
// the Record method. It returns false when the iteration stops, either by reaching the end of the
// input or an error. After Next returns false, the Error method will return any error that
// occurred during iteration, except that if it was io.EOF, Error will return nil.
func (i *Iterator) Next() bool {
	if i.err != nil {
		return false
	}
	i.rec, i.err = i.r.Read()
	return i.err == nil
}

// Error returns the first non-EOF error that was encountered by the Iterator.
func (i *Iterator) Error() error {
	if i.err == io.EOF {
		return nil
	}
	return i.err
}

// Record returns the most recent record read by a call to Next.
func (i *Iterator) Record() *Record { return i.rec }

type Writer struct {
	w     io.Writer
	flags int
}

func NewWriter(w io.Writer, h *Header, flags int) (*Writer, error) {
	if flags < FlagDecimal || flags > FlagString {
		return nil, errors.New("bam: flag format option out of range")
	}
	sw := &Writer{w: w, flags: flags}
	text, _ := h.MarshalText()
	_, err := w.Write(text)
	if err != nil {
		return nil, err
	}
	return sw, nil
}

func (w *Writer) Write(r *Record) error {
	b, err := r.MarshalSAM(w.flags)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.w.Write(b)
	return err
}

const (
	wordBits = 31

	maxInt32 = int(int32(^uint32(0) >> 1))
	minInt32 = -int(maxInt32) - 1
)

func validInt32(i int) bool { return minInt32 <= i && i <= maxInt32 }

func validLen(i int) bool      { return 1 <= i && i <= 1<<wordBits-1 }
func validPos(i int) bool      { return -1 <= i && i <= (1<<wordBits-1)-1 } // 0-based.
func validTmpltLen(i int) bool { return -(1<<wordBits) <= i && i <= 1<<wordBits-1 }

const nextBinShift = 3

const (
	level0 = uint16(((1 << (iota * nextBinShift)) - 1) / 7)
	level1
	level2
	level3
	level4
	level5
)

const indexWordBits = 29

func validIndexPos(i int) bool { return -1 <= i && i <= (1<<indexWordBits-1)-1 } // 0-based.

const (
	level0Shift = indexWordBits - (iota * nextBinShift)
	level1Shift
	level2Shift
	level3Shift
	level4Shift
	level5Shift
)

// calculate bin given an alignment covering [beg,end) (zero-based, half-close-half-open)
func reg2bin(beg, end int) uint16 {
	end--
	switch {
	case beg>>level5Shift == end>>level5Shift:
		return level5 + uint16(beg>>level5Shift)
	case beg>>level4Shift == end>>level4Shift:
		return level4 + uint16(beg>>level4Shift)
	case beg>>level3Shift == end>>level3Shift:
		return level3 + uint16(beg>>level3Shift)
	case beg>>level2Shift == end>>level2Shift:
		return level2 + uint16(beg>>level2Shift)
	case beg>>level1Shift == end>>level1Shift:
		return level1 + uint16(beg>>level1Shift)
	}
	return level0
}
