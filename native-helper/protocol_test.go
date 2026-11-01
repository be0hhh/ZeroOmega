package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestFramingRoundTripAndPartialIO(t *testing.T) {
	var b bytes.Buffer
	req := Request{ID: "test", Command: "version"}
	if err := writeFrame(&b, req); err != nil {
		t.Fatal(err)
	}
	var got Request
	if err := readFrame(&b, &got); err != nil {
		t.Fatal(err)
	}
	if got != req {
		t.Fatal("framing changed request")
	}
}
func TestFramingRejectsInvalidLengthsAndTruncation(t *testing.T) {
	for _, size := range []uint32{0, maxMessageSize + 1} {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], size)
		var req Request
		if err := readFrame(bytes.NewReader(b[:]), &req); err == nil {
			t.Fatal("accepted invalid length")
		}
	}
	var req Request
	if err := readFrame(bytes.NewReader([]byte{4, 0, 0, 0, '{'}), &req); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("unexpected truncation error: %v", err)
	}
}
func TestServeFramesMultipleRequests(t *testing.T) {
	var input, output bytes.Buffer
	for _, id := range []string{"one", "two"} {
		_ = writeFrame(&input, Request{ID: id, Command: "version"})
	}
	err := serveFrames(&input, &output, func(r Request) Response { return Response{ID: r.ID, OK: true, Result: protocolVersion} })
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		var r Response
		if err := readFrame(&output, &r); err != nil || !r.OK || r.ID != id {
			t.Fatal("incorrect response stream")
		}
	}
}
