//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func currentSID() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}
func pipeIdentity() (string, string, error) {
	sid, err := currentSID()
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(sid))
	return `\\.\pipe\zeroomega-wg-` + hex.EncodeToString(sum[:12]), sid, nil
}
func protect(data []byte) ([]byte, error)   { return cryptData(data, true) }
func unprotect(data []byte) ([]byte, error) { return cryptData(data, false) }
func cryptData(data []byte, encrypt bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty DPAPI input")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	var err error
	if encrypt {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...), nil
}
func configStorePath() (string, error) {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		return "", errors.New("LOCALAPPDATA is unavailable")
	}
	return filepath.Join(dir, "ZeroOmegaWG", "configs", "store.dpapi"), nil
}
func platformMain() error {
	if len(os.Args) == 2 && os.Args[1] == "--start" {
		return checkBroker(true)
	}
	if len(os.Args) == 2 && os.Args[1] == "--check" {
		return checkBroker(false)
	}
	if len(os.Args) == 2 && os.Args[1] == "--broker" {
		return runBroker()
	}
	if len(os.Args) == 2 && os.Args[1] == "--shutdown" {
		return shutdownBroker()
	}
	// Browser host manifests enforce the allowed extension IDs. No TCP control API
	// is exposed. Native callers have the same authority as the current OS user.
	return runBridge(os.Stdin, os.Stdout)
}

// A successful pipe connection alone does not prove the broker loaded its
// configuration store. Wait for its framed version response before reporting
// installation/startup success. The broker outlives this short-lived client.
func checkBroker(start bool) error {
	c, _, err := connectBroker(start)
	if err != nil {
		return err
	}
	defer c.Close()
	return checkBrokerConnection(c)
}

func checkBrokerConnection(c net.Conn) error {
	if err := c.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return err
	}
	if err := writeFrame(c, Request{ID: "health", Command: "version"}); err != nil {
		return err
	}
	var response Response
	if err := readFrame(c, &response); err != nil {
		return err
	}
	if !response.OK || response.ID != "health" {
		return errors.New("companion health check failed")
	}
	raw, err := json.Marshal(response.Result)
	if err != nil {
		return err
	}
	var version struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := json.Unmarshal(raw, &version); err != nil || version.ProtocolVersion != protocolVersion {
		return errors.New("companion protocol version is incompatible")
	}
	return nil
}
func connectBroker(start bool) (net.Conn, uint32, error) {
	name, sid, err := pipeIdentity()
	if err != nil {
		return nil, 0, err
	}
	dial := func() (net.Conn, uint32, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		c, err := winio.DialPipeContext(ctx, name)
		if err != nil {
			return nil, 0, err
		}
		pid, err := verifyServer(c, sid)
		if err != nil {
			c.Close()
			return nil, 0, err
		}
		return c, pid, nil
	}
	c, pid, err := dial()
	if err == nil || !start {
		return c, pid, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, 0, err
	}
	cmd := exec.Command(exe, "--broker")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW | windows.DETACHED_PROCESS}
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	_ = cmd.Process.Release()
	for attempt := 0; attempt < 40; attempt++ {
		c, pid, err = dial()
		if err == nil {
			return c, pid, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, 0, errors.New("could not start native companion broker")
}
func verifyServer(c net.Conn, expectedSID string) (uint32, error) {
	fd, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return 0, errors.New("could not authenticate broker pipe")
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(fd.Fd()), &pid); err != nil {
		return 0, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return 0, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return 0, err
	}
	if user.User.Sid.String() != expectedSID {
		return 0, errors.New("broker pipe belongs to another user")
	}
	return pid, nil
}
func runBridge(r io.Reader, w io.Writer) error {
	// Keep one pipe open per native host; the broker is shared between browser
	// processes. Serialize replies through this stream, never log to stdout.
	var c net.Conn
	defer func() {
		if c != nil {
			c.Close()
		}
	}()
	return serveFrames(r, w, func(req Request) Response {
		switch req.Command {
		case "version", "import", "connect", "disconnect", "status", "delete":
		default:
			return failure(req.ID, "unknown_command", "Unsupported helper command.")
		}
		if c == nil {
			var err error
			c, _, err = connectBroker(true)
			if err != nil {
				return failure(req.ID, "broker_unavailable", "Could not start companion. Reinstall the Windows helper and retry.")
			}
		}
		_ = c.SetDeadline(time.Now().Add(20 * time.Second))
		if err := writeFrame(c, req); err != nil {
			c.Close()
			c = nil
			return failure(req.ID, "broker_unavailable", "Companion stopped. Retry the operation.")
		}
		var response Response
		if err := readFrame(c, &response); err != nil {
			c.Close()
			c = nil
			return failure(req.ID, "broker_unavailable", "Companion stopped. Retry the operation.")
		}
		return response
	})
}
func runBroker() error {
	name, sid, err := pipeIdentity()
	if err != nil {
		return err
	}
	path, err := configStorePath()
	if err != nil {
		return err
	}
	return runBrokerAt(name, sid, path)
}
func runBrokerAt(name, sid, path string) error {
	listener, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;" + sid + ")", InputBufferSize: 65536, OutputBufferSize: 65536})
	if err != nil {
		return err
	}
	defer listener.Close()
	m, err := newManager(protectedStore{path: path})
	if err != nil {
		return err
	}
	defer m.Close()
	var mu sync.Mutex
	clients := map[net.Conn]bool{}
	var shutdown sync.Once
	done := make(chan struct{})
	var wg sync.WaitGroup
	stop := func() {
		shutdown.Do(func() {
			close(done)
			listener.Close()
			mu.Lock()
			for c := range clients {
				c.Close()
			}
			mu.Unlock()
		})
	}
	defer func() { stop(); wg.Wait() }()
	for {
		c, err := listener.Accept()
		if err != nil {
			select {
			case <-done:
				return nil
			default:
				return err
			}
		}
		mu.Lock()
		select {
		case <-done:
			mu.Unlock()
			c.Close()
			return nil
		default:
		}
		clients[c] = true
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { c.Close(); mu.Lock(); delete(clients, c); mu.Unlock() }()
			for {
				var req Request
				if err := readFrame(c, &req); err != nil {
					return
				}
				if req.Command == "__shutdown" {
					// Only same-user clients can reach this pipe; bridge does not forward this command.
					m.Close()
					_ = writeFrame(c, Response{ID: req.ID, OK: true, Result: map[string]bool{"stopped": true}})
					stop()
					return
				}
				if err := writeFrame(c, m.Handle(req)); err != nil {
					return
				}
			}
		}()
	}
}
func shutdownBroker() error {
	c, pid, err := connectBroker(false)
	if err != nil {
		// If no listener exists, shutdown is already complete. Access errors are not
		// treated as success: an upgrade must not race an unauthenticated process.
		name, _, identityErr := pipeIdentity()
		if identityErr != nil {
			return identityErr
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		probe, e := winio.DialPipeContext(ctx, name)
		if e == nil {
			probe.Close()
			return err
		}
		if errors.Is(e, syscall.ERROR_FILE_NOT_FOUND) || errors.Is(e, syscall.ERROR_PATH_NOT_FOUND) {
			return nil
		}
		return e
	}
	defer c.Close()
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if err := writeFrame(c, Request{ID: "shutdown", Command: "__shutdown"}); err != nil {
		return err
	}
	var resp Response
	if err := readFrame(c, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return errors.New("broker refused shutdown")
	}
	c.Close()
	result, err := windows.WaitForSingleObject(process, 10000)
	if err != nil {
		return err
	}
	if result != windows.WAIT_OBJECT_0 {
		return errors.New("broker did not exit before timeout")
	}
	return nil
}
