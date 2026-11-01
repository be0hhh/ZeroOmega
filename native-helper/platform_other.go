//go:build !windows

package main

import "errors"

func platformMain() error              { return errors.New("the companion is supported on Windows only") }
func protect([]byte) ([]byte, error)   { return nil, errors.New("DPAPI requires Windows") }
func unprotect([]byte) ([]byte, error) { return nil, errors.New("DPAPI requires Windows") }
