//go:build windows

package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// The builder supplies the exact distribution ZIP in a temporary build folder.
//
//go:embed payload.zip
var payload []byte

func main() {
	noOpen := false
	for _, argument := range os.Args[1:] {
		if strings.EqualFold(argument, "--NoOpen") || strings.EqualFold(argument, "-NoOpen") {
			noOpen = true
		} else {
			reportFailure(errors.New("Unknown argument. Use --NoOpen for unattended installation."), false)
		}
	}
	if err := install(noOpen); err != nil {
		reportFailure(err, noOpen)
	}
	if !noOpen {
		messageBox("ZeroOmega WG", "ZeroOmega WG установлен. Помощник запущен.\nСледуйте открывшейся инструкции, чтобы добавить расширение в Chrome.\n\nInstalled. The companion is running.\nFollow the opened instructions to add the Chrome extension.\n\nFirefox: для постоянной установки нужна подпись Mozilla.\nFirefox requires a Mozilla-signed XPI for permanent installation.", 0x40)
	}
}

func install(noOpen bool) error {
	temporary, err := os.MkdirTemp("", "zeroomega-wg-setup-")
	if err != nil {
		return fmt.Errorf("cannot create temporary installer directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	kit, err := extractPayload(payload, temporary)
	if err != nil {
		return err
	}
	windowsRoot := os.Getenv("SystemRoot")
	if windowsRoot == "" {
		return errors.New("SystemRoot is missing; Windows PowerShell cannot be located")
	}
	powershell := filepath.Join(windowsRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	arguments := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(kit, "install.ps1")}
	if noOpen {
		arguments = append(arguments, "-NoOpen")
	}
	command := exec.Command(powershell, arguments...)
	command.Dir = kit
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	output, runErr := command.CombinedOutput()
	application := filepath.Join(os.Getenv("LOCALAPPDATA"), "ZeroOmegaWG")
	if os.Getenv("LOCALAPPDATA") != "" {
		_ = os.MkdirAll(application, 0700)
		_ = os.WriteFile(filepath.Join(application, "setup.log"), output, 0600)
	}
	if runErr != nil {
		return fmt.Errorf("installation failed: %w\n%s\nLog: %s", runErr, strings.TrimSpace(string(output)), filepath.Join(application, "setup.log"))
	}
	return nil
}

func reportFailure(err error, noOpen bool) {
	message := "Установка не завершена / Installation did not complete.\n\n" + err.Error()
	_, _ = fmt.Fprintln(os.Stderr, message)
	if !noOpen {
		messageBox("ZeroOmega WG — ошибка / error", message, 0x10)
	}
	os.Exit(1)
}

func messageBox(title, text string, flags uintptr) {
	titlePointer, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	textPointer, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	user32 := syscall.NewLazyDLL("user32.dll")
	_, _, _ = user32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(textPointer)), uintptr(unsafe.Pointer(titlePointer)), flags)
}
