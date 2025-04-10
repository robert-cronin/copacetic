package push

import (
	"flag"
	"os"
	"testing"
)

var (
	copaPath     string
	buildkitAddr string
	ghcrBasePath string
	runID        string
)

func TestMain(m *testing.M) {
	flag.StringVar(&buildkitAddr, "addr", "buildx://", "buildkit address to pass through to copa binary")
	flag.StringVar(&copaPath, "copa", "./copa", "path to copa binary")
	flag.StringVar(&ghcrBasePath, "ghcr-base-path", "ghcr.io/robert-cronin/copacetic", "base path for ghcr")
	flag.StringVar(&runID, "run-id", "", "run ID for the test")
	flag.Parse()

	if copaPath == "" {
		panic("missing --copa")
	}

	if runID == "" {
		panic("missing --run-id")
	}

	ec := m.Run()
	os.Exit(ec)
}
