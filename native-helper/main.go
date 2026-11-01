package main

import "os"

func main() {
	if err := platformMain(); err != nil {
		os.Exit(1)
	}
}
