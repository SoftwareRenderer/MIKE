package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// frameHeader builds the 9-byte header a peer would send.
func frameHeader(msgID uint32, typ uint8, length uint32) []byte {
	h := make([]byte, HeaderSize)
	binary.BigEndian.PutUint32(h[0:4], msgID)
	h[4] = typ
	binary.BigEndian.PutUint32(h[5:9], length)
	return h
}

// quietReader returns what it holds, then pauses before reporting EOF.
type quietReader struct{ buf []byte }

func (q *quietReader) Read(p []byte) (int, error) {
	if len(q.buf) > 0 {
		n := copy(p, q.buf)
		q.buf = q.buf[n:]
		return n, nil
	}
	time.Sleep(50 * time.Millisecond)
	return 0, io.EOF
}

// A declared length is not evidence. ReadFrame must refuse before allocating from it: the field
// is 4 bytes, so a peer that announces a payload and never sends one still costs the whole claim.
func TestReadFrameRefusesAnOversizedDeclaredPayloadWithoutAllocating(t *testing.T) {
	claim := uint32(MaxFramePayload + 1)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := ReadFrame(&quietReader{buf: frameHeader(1, TypeResult, claim)})
	runtime.ReadMemStats(&after)

	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("want ErrFrameTooLarge, got %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(MaxFramePayload)) {
		t.Errorf("the refusal must name the limit, got: %v", err)
	}
	// The heap is measured as well as the error: a refusal raised after the make would satisfy
	// errors.Is and still have spent the claim.
	if allocated := int64(after.TotalAlloc) - int64(before.TotalAlloc); allocated > 1<<20 {
		t.Errorf("refusal allocated %d bytes from a length that never arrived", allocated)
	}
}

// The cap is a ceiling, not a default: a frame within it reads whole.
func TestReadFrameReadsAPayloadWithinTheCap(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 4096)
	r := &quietReader{buf: append(frameHeader(7, TypeResult, uint32(len(payload))), payload...)}

	frame, err := ReadFrame(r)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if frame.MsgID != 7 || !bytes.Equal(frame.Payload, payload) {
		t.Errorf("frame = {MsgID %d, %d bytes}, want {7, %d bytes}", frame.MsgID, len(frame.Payload), len(payload))
	}
}

// Reads must hit MaxReadBytes rather than the looser frame cap.
func TestMaxReadBytesIsBelowTheFrameCap(t *testing.T) {
	if MaxReadBytes > MaxFramePayload {
		t.Errorf("MaxReadBytes (%d) must not exceed MaxFramePayload (%d): the smaller bound is the one reads hit",
			MaxReadBytes, MaxFramePayload)
	}
}
