package manifest

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/project-copacetic/copacetic/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverPlatforms(t *testing.T) {
	// Create a temporary test directory
	tempDir, err := os.MkdirTemp("", "copa-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Mock the imageRef handling with a test helper that returns pre-defined platforms
	origDiscoverImagePlatforms := discoverImagePlatforms
	defer func() { discoverImagePlatforms = origDiscoverImagePlatforms }()
	
	discoverImagePlatforms = func(imageRef string) ([]types.Platform, error) {
		return []types.Platform{
			{OS: "linux", Arch: "amd64", Digest: "sha256:amd64digest"},
			{OS: "linux", Arch: "arm64", Digest: "sha256:arm64digest"},
			{OS: "windows", Arch: "amd64", Digest: "sha256:winamd64digest"},
		}, nil
	}

	// Create test report files
	createTestReportFile(t, tempDir, "report-linux-amd64.json", "amd64 report content")
	createTestReportFile(t, tempDir, "report-linux-arm64.json", "arm64 report content")
	// Intentionally omit the Windows report

	// Override the reportFileRegex for testing
	origReportFileRegex := reportFileRegex
	defer func() { reportFileRegex = origReportFileRegex }()
	
	// Create a simple regex that matches our test files
	reportFileRegex = regexp.MustCompile(`report-([a-z]+)-([a-z0-9]+)\.json`)

	// Call the function under test
	platforms, err := DiscoverPlatforms("test/image:latest", tempDir)
	require.NoError(t, err)

	// Verify the results
	require.Len(t, platforms, 2, "Should find two patchable platforms")
	
	// Check that we got the expected platforms with report paths
	assert.Equal(t, "linux", platforms[0].OS)
	assert.Equal(t, "amd64", platforms[0].Arch)
	assert.Equal(t, "sha256:amd64digest", platforms[0].Digest)
	assert.Contains(t, platforms[0].ReportPath, "report-linux-amd64.json")
	
	assert.Equal(t, "linux", platforms[1].OS)
	assert.Equal(t, "arm64", platforms[1].Arch)
	assert.Equal(t, "sha256:arm64digest", platforms[1].Digest)
	assert.Contains(t, platforms[1].ReportPath, "report-linux-arm64.json")
}

// Helper to create a test report file
func createTestReportFile(t *testing.T, dir, filename, content string) {
	path := filepath.Join(dir, filename)
	err := os.WriteFile(path, []byte(content), 0644)
	require.NoError(t, err)
}

func TestDiscoverReportsInDirectory(t *testing.T) {
	// Create a temporary test directory
	tempDir, err := os.MkdirTemp("", "copa-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Create test report files
	createTestReportFile(t, tempDir, "report-linux-amd64.json", "amd64 report content")
	createTestReportFile(t, tempDir, "report-linux-arm64.json", "arm64 report content")
	createTestReportFile(t, tempDir, "other-file.txt", "not a report")
	
	// Create a subdirectory (which should be ignored)
	subDir := filepath.Join(tempDir, "subdir")
	err = os.Mkdir(subDir, 0755)
	require.NoError(t, err)

	// Override the reportFileRegex for testing
	origReportFileRegex := reportFileRegex
	defer func() { reportFileRegex = origReportFileRegex }()
	
	// Create a simple regex that matches our test files
	reportFileRegex = regexp.MustCompile(`report-([a-z]+)-([a-z0-9]+)\.json`)

	// Call the function under test
	reports, err := discoverReportsInDirectory(tempDir)
	require.NoError(t, err)

	// Verify the results
	require.Len(t, reports, 2, "Should find two report files")
	
	// Check that we got the expected reports
	assert.Contains(t, reports, "linux/amd64")
	assert.Contains(t, reports, "linux/arm64")
	assert.Contains(t, reports["linux/amd64"], "report-linux-amd64.json")
	assert.Contains(t, reports["linux/arm64"], "report-linux-arm64.json")
} 