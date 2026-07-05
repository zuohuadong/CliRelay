package main

import (
	"fmt"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/videotool"
)

func main() {
	if err := (videotool.CLI{}).RunVideo(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
