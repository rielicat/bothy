package engine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Stream ids in the multiplexed exec stream.
const (
	Stdin  = 0
	Stdout = 1
	Stderr = 2
	// systemErr frames carry engine errors (API >= 1.41).
	systemErr = 3
)

const demuxChunk = 32 << 10

// Demux reads the engine's multiplexed stream (8-byte headers: stream id,
// three zero bytes, big-endian payload length) and hands payloads to fn in
// chunks of at most 32 KiB. It returns nil at a clean EOF between frames.
func Demux(r io.Reader, fn func(stream int, p []byte) error) error {
	var hdr [8]byte
	buf := make([]byte, demuxChunk)
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("engine: stream header: %w", err)
		}
		stream := int(hdr[0])
		if stream > systemErr || hdr[1] != 0 || hdr[2] != 0 || hdr[3] != 0 {
			return fmt.Errorf("engine: malformed stream header % x", hdr)
		}
		size := binary.BigEndian.Uint32(hdr[4:])
		if stream == systemErr {
			msg := make([]byte, min(size, 4<<10))
			if _, err := io.ReadFull(r, msg); err != nil {
				return fmt.Errorf("engine: stream error frame: %w", err)
			}
			if _, err := io.CopyN(io.Discard, r, int64(size)-int64(len(msg))); err != nil {
				return fmt.Errorf("engine: stream error frame: %w", err)
			}
			return fmt.Errorf("engine: %s", msg)
		}
		for size > 0 {
			n := min(size, uint32(len(buf)))
			if _, err := io.ReadFull(r, buf[:n]); err != nil {
				return fmt.Errorf("engine: stream payload: %w", err)
			}
			size -= n
			if stream == Stdin {
				continue
			}
			if err := fn(stream, buf[:n]); err != nil {
				return err
			}
		}
	}
}

// AppendFrame appends one multiplexed frame; used by tests and fakes.
func AppendFrame(dst []byte, stream int, p []byte) []byte {
	var hdr [8]byte
	hdr[0] = byte(stream)
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(p)))
	return append(append(dst, hdr[:]...), p...)
}
