package link

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// DefaultMaxLineSize is the maximum size of a single JSON-lines message (1 MB).
const DefaultMaxLineSize = 1 << 20

// Encoder writes LinkMessages as newline-delimited JSON to an io.Writer.
// It is safe for concurrent use.
type Encoder struct {
	mu sync.Mutex
	w  io.Writer
}

// NewEncoder returns an Encoder that writes to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w}
}

// Encode marshals msg as JSON and writes it followed by a newline.
func (e *Encoder) Encode(msg LinkMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("link: marshal: %w", err)
	}
	data = append(data, '\n')

	e.mu.Lock()
	defer e.mu.Unlock()

	_, err = e.w.Write(data)
	if err != nil {
		return fmt.Errorf("link: write: %w", err)
	}
	return nil
}

// Decoder reads LinkMessages from a newline-delimited JSON stream.
type Decoder struct {
	scanner *bufio.Scanner
}

// NewDecoder returns a Decoder that reads from r.
// Lines longer than DefaultMaxLineSize are rejected.
func NewDecoder(r io.Reader) *Decoder {
	return NewDecoderSize(r, DefaultMaxLineSize)
}

// NewDecoderSize returns a Decoder with a custom maximum line size.
func NewDecoderSize(r io.Reader, maxLineSize int) *Decoder {
	s := bufio.NewScanner(r)
	initSize := 4096
	if initSize > maxLineSize {
		initSize = maxLineSize
	}
	s.Buffer(make([]byte, 0, initSize), maxLineSize)
	return &Decoder{scanner: s}
}

// Decode reads the next JSON-lines message and unmarshals it.
// Returns io.EOF when the underlying reader is closed cleanly.
func (d *Decoder) Decode() (LinkMessage, error) {
	if !d.scanner.Scan() {
		if err := d.scanner.Err(); err != nil {
			return LinkMessage{}, fmt.Errorf("link: scan: %w", err)
		}
		return LinkMessage{}, io.EOF
	}

	line := d.scanner.Bytes()
	var msg LinkMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return LinkMessage{}, fmt.Errorf("link: unmarshal: %w", err)
	}
	return msg, nil
}
