package push

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPushToRegistry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	uniqueTag := fmt.Sprintf("run-%s", runID)
	baseImage := "docker.io/library/nginx:1.21.6"
	ghcrImageBase := fmt.Sprintf("%s:%s", ghcrBasePath, uniqueTag)
	ghcrImagePatched := fmt.Sprintf("%s-patched", ghcrImageBase) // Adds '-patched' before the tag

	t.Logf("Base GHCR Image: %s", ghcrImageBase)
	t.Logf("Patched GHCR Image: %s", ghcrImagePatched)

	// Push base image to GHCR using oras
	// Assumes authentication is handled externally (by docker/login-action in CI)
	t.Logf("Copying base image %s to %s", baseImage, ghcrImageBase)
	pushCmd := exec.Command("oras", "cp", baseImage, ghcrImageBase)
	out, err := pushCmd.CombinedOutput()
	require.NoErrorf(t, err, "oras cp to ghcr failed:\n%s", string(out))

	// create a temp directory for report file
	cwd, err := os.Getwd()
	require.NoError(t, err, "failed to get current working directory")
	reportFile := filepath.Join(cwd, "testdata", "report.json")

	// run copa patch with push flag using BuildKit address for the source image
	patchCmd := exec.Command(
		copaPath,
		"patch",
		"--image", ghcrImageBase,
		"--report", reportFile,
		"--push",
		"--tag", "patched", // copa will push to buildkitRegistryHost/nginx:patched
		"-a="+buildkitAddr,
	)

	output, err := patchCmd.CombinedOutput()
	// Use NoErrorf for better output formatting on failure
	require.NoErrorf(t, err, "failed to patch and push image: %s", string(output))

	// // Check it exists in GHCR by pulling it
	// // Ensure local docker is logged in if running locally, CI handles this
	// t.Logf("Verifying patched image by pulling %s", ghcrImagePatched)
	// pullCmd := exec.Command("docker", "pull", ghcrImagePatched)
	// output, err = pullCmd.CombinedOutput()
	// require.NoErrorf(t, err, "failed to pull patched image from ghcr: %s", string(output))

	// // Clean up local docker cache (optional, doesn't delete from GHCR)
	// removeLocalImage(t, ghcrImageBase)
	// removeLocalImage(t, ghcrImagePatched)

}
