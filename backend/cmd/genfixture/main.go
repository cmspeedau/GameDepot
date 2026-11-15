package main

import (
	"fmt"
	"os"

	"github.com/cmspeedau/gamedepot/internal/depot"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: genfixture <srcdir> <depotroot>")
		os.Exit(2)
	}
	src, root := os.Args[1], os.Args[2]
	digest, err := depot.BuildRelease(src, root, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(digest)
}
