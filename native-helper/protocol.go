package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

const maxMessageSize = 1 << 20
const protocolVersion = 1
const helperVersion = "0.1.2"

type Request struct {
	ID       string `json:"id"`
	Command  string `json:"command"`
	ConfigID string `json:"configId,omitempty"`
	Config   string `json:"config,omitempty"`
}
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Response struct {
	ID     string    `json:"id"`
	OK     bool      `json:"ok"`
	Result any       `json:"result,omitempty"`
	Error  *APIError `json:"error,omitempty"`
}
type ProfileStatus struct {
	ConfigID string `json:"configId"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	State    string `json:"state"`
	Error    string `json:"error,omitempty"`
}

func failure(id, code, message string) Response {
	return Response{ID: id, Error: &APIError{Code: code, Message: message}}
}
func readFrame(r io.Reader, value any) error {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return err
	}
	n := binary.LittleEndian.Uint32(head[:])
	if n == 0 || n > maxMessageSize {
		return errors.New("invalid message length")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	return json.Unmarshal(b, value)
}
func writeFrame(w io.Writer, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(b) > maxMessageSize {
		return errors.New("message too large")
	}
	var head [4]byte
	binary.LittleEndian.PutUint32(head[:], uint32(len(b)))
	if err := writeAll(w, head[:]); err != nil {
		return err
	}
	return writeAll(w, b)
}
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func serveFrames(r io.Reader, w io.Writer, handle func(Request) Response) error {
	for {
		var req Request
		if err := readFrame(r, &req); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := writeFrame(w, handle(req)); err != nil {
			return err
		}
	}
}
