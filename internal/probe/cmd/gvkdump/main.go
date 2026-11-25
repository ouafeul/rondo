package main

import (
	"bufio"
	"fmt"
	"os"

	"rondo/internal/probe"
)

func main() {
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	for _, k := range probe.ModelledKinds() {
		fmt.Fprintln(w, k)
	}
}
