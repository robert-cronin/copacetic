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
	patchedTag := fmt.Sprintf("%s-patched", uniqueTag)
	baseImage := "docker.io/library/nginx:1.21.6"
	ghcrImageBase := fmt.Sprintf("%s:%s", ghcrBasePath, uniqueTag)
	ghcrImagePatched := fmt.Sprintf("%s:%s", ghcrBasePath, patchedTag)

	t.Logf("Base GHCR Image: %s", ghcrImageBase)
	t.Logf("Patched GHCR Image: %s", ghcrImagePatched)

	// push base image to ghcr using oras
	t.Logf("Copying base image %s to %s", baseImage, ghcrImageBase)
	cpCmd := exec.Command("oras", "cp", baseImage, ghcrImageBase)
	out, err := cpCmd.CombinedOutput()
	require.NoErrorf(t, err, "oras cp to ghcr failed:\n%s", string(out))

	cwd, err := os.Getwd()
	reportFile := filepath.Join(cwd, "testdata", "report.json")

	// run copa patch with push flag using BuildKit address for the source image
	patchCmd := exec.Command(
		copaPath,
		"patch",
		"--image", ghcrImageBase,
		"--report", reportFile,
		"--push=true",
		"--tag", patchedTag,
		"-a="+buildkitAddr,
	)

	output, err := patchCmd.CombinedOutput()
	require.NoErrorf(t, err, "failed to patch and push image: %s", string(output))
	t.Logf("Patch output: %s\n", string(output))

	// check it exists in ghcr by pulling it
	t.Logf("Verifying patched image by pulling %s", ghcrImagePatched)
	pullCmd := exec.Command("docker", "pull", ghcrImagePatched)
	output, err = pullCmd.CombinedOutput()
	require.NoErrorf(t, err, "failed to pull patched image from ghcr: %s", string(output))
}
