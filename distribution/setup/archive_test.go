package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validPayload(t *testing.T, extra map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	files := map[string]string{
		"install.ps1": "install", "uninstall.ps1": "uninstall", "zeroomega-wg.exe": "binary",
		"chrome/manifest.json": "{}", "chrome-extension-id.txt": "identifier",
		"firefox/zeroomega-wg-unsigned.xpi": "extension", "START-HERE.html": "help",
	}
	for name, contents := range extra {
		files[name] = contents
	}
	for name, contents := range files {
		entry, err := writer.Create(kitRoot + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestExtractPayload(t *testing.T) {
	directory := t.TempDir()
	root, err := extractPayload(validPayload(t, map[string]string{"chrome/js/module.js": "code"}), directory)
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(directory, kitRoot) {
		t.Fatal("unexpected package root")
	}
	contents, err := os.ReadFile(filepath.Join(root, "chrome", "js", "module.js"))
	if err != nil || string(contents) != "code" {
		t.Fatalf("nested payload not extracted: %s, %v", contents, err)
	}
}

func TestRejectUnsafePathsBeforeExtraction(t *testing.T) {
	for _, name := range []string{
		"../escape", "chrome/../../escape", "/absolute", "chrome\\escape", "chrome/file:stream",
		"chrome/NUL.txt", "chrome/CON", "chrome/COM1.js", "chrome/COM¹.js", "chrome/CON .txt", "chrome/file.", "chrome/file ",
		"chrome/./module", "chrome//module", "chrome/file?", "unexpected.exe",
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			_, err := extractPayload(validPayload(t, map[string]string{name: "bad"}), directory)
			if err == nil {
				t.Fatal("unsafe filename accepted")
			}
			files, err := os.ReadDir(directory)
			if err != nil || len(files) != 0 {
				t.Fatal("archive was partially extracted before validation")
			}
		})
	}
}

func TestRejectCaseCollisions(t *testing.T) {
	_, err := extractPayload(validPayload(t, map[string]string{"chrome/MANIFEST.JSON": "collision"}), t.TempDir())
	if err == nil {
		t.Fatal("case-insensitive path collision accepted")
	}
}

func TestRejectSymlinkAndOversize(t *testing.T) {
	payload := validPayload(t, nil)
	for _, scenario := range []string{"symlink", "oversize", "total-size", "missing-file", "directory-as-required-file"} {
		t.Run(scenario, func(t *testing.T) {
			reader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "symlink":
				reader.File[0].SetMode(os.ModeSymlink | 0777)
			case "oversize":
				reader.File[0].UncompressedSize64 = maxFileSize + 1
			case "total-size":
				for index := 0; index < 5; index++ {
					reader.File[index].UncompressedSize64 = maxFileSize
				}
			case "missing-file":
				reader.File = reader.File[1:]
			case "directory-as-required-file":
				reader.File[0].SetMode(os.ModeDir | 0700)
			}
			if err := validateArchive(reader.File); err == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
}

func TestRejectCorruptPayload(t *testing.T) {
	if _, err := extractPayload([]byte("not an archive"), t.TempDir()); err == nil {
		t.Fatal("invalid ZIP accepted")
	}
	_, err := extractPayload(validPayload(t, nil), t.TempDir())
	if err != nil && strings.Contains(err.Error(), "missing") {
		t.Fatal("valid package required files rejected")
	}
}
