package slacktest

import (
	"bufio"
	"crypto/sha1" //nolint:gosec // RFC 6455 fixes SHA-1 for the handshake accept key
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// The fake speaks just enough RFC 6455 for a Socket Mode client: it reads masked
// client frames (reassembling fragments), answers pings and writes
// unfragmented, unmasked server frames. The server side is small enough not
// to need gorilla/websocket.

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xa

	maxFrame = 1 << 20
	wsGUID   = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
)

// errPeerClosed is returned by readMessage after the client sent a close frame.
var errPeerClosed = errors.New("websocket: peer closed the connection")

type wsConn struct {
	nc  net.Conn
	br  *bufio.Reader
	wmu sync.Mutex
}

func upgrade(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || key == "" || r.Header.Get("Sec-WebSocket-Version") != "13" {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return nil, errors.New("not a websocket upgrade")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return nil, errors.New("response writer cannot hijack")
	}
	nc, brw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum([]byte(key + wsGUID)) //nolint:gosec // see import
	accept := base64.StdEncoding.EncodeToString(sum[:])
	if _, err := fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept); err != nil {
		_ = nc.Close()
		return nil, err
	}
	if err := brw.Flush(); err != nil {
		_ = nc.Close()
		return nil, err
	}
	return &wsConn{nc: nc, br: brw.Reader}, nil
}

// readMessage returns the next complete data message.
func (c *wsConn) readMessage() ([]byte, error) {
	var msg []byte
	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch op {
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return nil, err
			}
		case opPong:
		case opClose:
			_ = c.writeFrame(opClose, payload)
			_ = c.nc.Close()
			return nil, errPeerClosed
		case opText, opBinary, opContinuation:
			msg = append(msg, payload...)
			if len(msg) > maxFrame {
				return nil, errors.New("websocket: message too large")
			}
			if fin {
				return msg, nil
			}
		default:
			return nil, fmt.Errorf("websocket: unknown opcode %d", op)
		}
	}
}

func (c *wsConn) readFrame() (fin bool, op byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.br, h[:]); err != nil {
		return false, 0, nil, err
	}
	fin, op = h[0]&0x80 != 0, h[0]&0x0f
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > maxFrame {
		return false, 0, nil, errors.New("websocket: frame too large")
	}
	var mask [4]byte
	masked := h[1]&0x80 != 0
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, op, payload, nil
}

func (c *wsConn) writeFrame(op byte, payload []byte) error {
	hdr := []byte{0x80 | op}
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n <= 0xffff:
		hdr = append(hdr, 126)
		hdr = binary.BigEndian.AppendUint16(hdr, uint16(n))
	default:
		hdr = append(hdr, 127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.nc.Write(append(hdr, payload...))
	return err
}

func (c *wsConn) writeText(b []byte) error { return c.writeFrame(opText, b) }

func (c *wsConn) ping() error { return c.writeFrame(opPing, []byte("ping")) }

// closeWith sends a close frame carrying code and drops the connection
// without waiting for the client's reply, as a server under load would.
func (c *wsConn) closeWith(code int, reason string) {
	payload := binary.BigEndian.AppendUint16(nil, uint16(code))
	_ = c.writeFrame(opClose, append(payload, reason...))
	_ = c.nc.Close()
}

func (c *wsConn) close() { _ = c.nc.Close() }
