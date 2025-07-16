package main

import (
	"os"

	"github.com/project-copacetic/copacetic/pkg/frontend/copa"
)

func main() {
	copa.RunFrontend(os.Args[1:])
}