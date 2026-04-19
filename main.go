package main

import (
	"fmt"
	"os"

	"xbot/serverapp"
)

func main() {
	if err := serverapp.Run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
