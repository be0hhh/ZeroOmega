package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	kitRoot        = "zeroomega-wg-windows-x64"
	maxArchiveSize = 256 << 20
	maxTotalSize   = 512 << 20
	maxFileSize    = 128 << 20
	maxFiles       = 20000
)

var topLevelFiles = map[string]bool{
	"zeroomega-wg.exe": true, "install.ps1": true, "uninstall.ps1": true,
	"chrome-extension-id.txt": true, "COPYING": true, "AUTHORS": true,
	"Go-LICENSE.txt": true,
	"README.ru.md":   true, "README.en.md": true, "START-HERE.html": true,
	"THIRD_PARTY_NOTICES.txt": true, "BUILD-INFO.json": true,
}

func validateArchive(entries []*zip.File) error {
	if len(entries) > maxFiles {
		return errors.New("too many installer files")
	}
	seen := make(map[string]bool)
	regular := make(map[string]bool)
	var total uint64
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name, "/")
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\:\x00") {
			return errors.New("unsafe installer archive path")
		}
		for _, segment := range strings.Split(name, "/") {
			if !safeWindowsSegment(segment) {
				return errors.New("unsafe Windows filename in installer")
			}
		}
		if entry.Mode()&os.ModeSymlink != 0 || (!entry.FileInfo().IsDir() && !entry.Mode().IsRegular()) {
			return errors.New("installer contains a link or unsupported file type")
		}
		if name == kitRoot && entry.FileInfo().IsDir() {
			continue
		}
		if !strings.HasPrefix(name, kitRoot+"/") {
			return errors.New("installer file is outside the application package")
		}
		relative := strings.TrimPrefix(name, kitRoot+"/")
		allowed := topLevelFiles[relative] || strings.HasPrefix(relative, "chrome/") ||
			relative == "firefox/zeroomega-wg-unsigned.xpi" || relative == "source/zeroomega-wg-source.zip" ||
			relative == "source/zeroomega-wg.patch"
		if entry.FileInfo().IsDir() && (relative == "chrome" || relative == "firefox" || relative == "source") {
			allowed = true
		}
		if !allowed {
			return fmt.Errorf("unexpected file in installer: %s", relative)
		}
		folded := strings.ToLower(name)
		if seen[folded] {
			return errors.New("duplicate installer filename")
		}
		seen[folded] = true
		regular[folded] = !entry.FileInfo().IsDir()
		if entry.UncompressedSize64 > maxFileSize || entry.UncompressedSize64 > maxTotalSize-total {
			return errors.New("installer exceeds extraction size limits")
		}
		total += entry.UncompressedSize64
	}
	for _, required := range []string{"install.ps1", "uninstall.ps1", "zeroomega-wg.exe", "chrome/manifest.json", "chrome-extension-id.txt", "firefox/zeroomega-wg-unsigned.xpi", "START-HERE.html"} {
		if !regular[strings.ToLower(kitRoot+"/"+required)] {
			return fmt.Errorf("installer is missing %s", required)
		}
	}
	return nil
}

func safeWindowsSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." || strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") {
		return false
	}
	for _, char := range segment {
		if char < 32 || strings.ContainsRune("<>\"|?*", char) {
			return false
		}
	}
	base := strings.ToUpper(strings.TrimRight(strings.SplitN(segment, ".", 2)[0], " "))
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" {
		return false
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		if base[3] >= '0' && base[3] <= '9' {
			return false
		}
	}
	for _, prefix := range []string{"COM", "LPT"} {
		for _, suffix := range []string{"¹", "²", "³"} {
			if base == prefix+suffix {
				return false
			}
		}
	}
	return true
}

func extractPayload(payload []byte, destination string) (string, error) {
	if len(payload) > maxArchiveSize {
		return "", errors.New("installer payload exceeds size limit")
	}
	archive, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return "", errors.New("installer payload is not a valid ZIP archive")
	}
	if err := validateArchive(archive.File); err != nil {
		return "", err
	}
	for _, entry := range archive.File {
		target := filepath.Join(destination, filepath.FromSlash(entry.Name))
		relative, err := filepath.Rel(destination, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return "", errors.New("installer path escaped temporary directory")
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0700); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return "", err
		}
		if err := extractFile(entry, target); err != nil {
			return "", err
		}
	}
	return filepath.Join(destination, kitRoot), nil
}

func extractFile(entry *zip.File, target string) error {
	input, err := entry.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, maxFileSize+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if written > maxFileSize || uint64(written) != entry.UncompressedSize64 {
		return errors.New("installer file exceeds declared size")
	}
	return closeErr
}
