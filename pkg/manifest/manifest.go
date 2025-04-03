package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/project-copacetic/copacetic/pkg/types"
)

// Regular expression for matching report files
var reportFileRegex = regexp.MustCompile(`report-([a-z]+)-([a-z0-9]+)\.json`)

// DiscoverPlatforms inspects an image reference to discover available platforms
// and matches them with available reports in the report directory
func DiscoverPlatforms(imageRef, reportDir string) ([]types.Platform, error) {
	// 1. Inspect the image reference to determine if it's a multi-arch image
	subManifests, err := discoverImagePlatforms(imageRef)
	if err != nil {
		return nil, fmt.Errorf("failed to discover platforms from image: %w", err)
	}

	// 2. Discover available reports in the report directory
	availableReports, err := discoverReportsInDirectory(reportDir)
	if err != nil {
		return nil, fmt.Errorf("failed to discover reports in directory: %w", err)
	}

	// 3. Find the intersection of platforms that have both a manifest and a report
	patchablePlatforms := findPatchablePlatforms(subManifests, availableReports)

	return patchablePlatforms, nil
}

// discoverImagePlatforms retrieves platform information from an image reference
var discoverImagePlatforms = func(imageRef string) ([]types.Platform, error) {
	// Use go-containerregistry to inspect the image and determine if it's a multi-arch image
	// If it is, extract the platform details for each sub-manifest

	// Example implementation:
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return nil, fmt.Errorf("parsing reference %q: %w", imageRef, err)
	}

	// Fetch the image descriptor
	desc, err := remote.Get(ref)
	if err != nil {
		return nil, fmt.Errorf("fetching descriptor for %q: %w", imageRef, err)
	}

	// Check if it's a manifest or an index (multi-arch)
	if desc.MediaType.IsIndex() {
		// It's a multi-arch image, retrieve the index
		index, err := desc.ImageIndex()
		if err != nil {
			return nil, fmt.Errorf("getting image index: %w", err)
		}

		// Retrieve the manifest
		indexManifest, err := index.IndexManifest()
		if err != nil {
			return nil, fmt.Errorf("getting index manifest: %w", err)
		}

		// Extract platform information for each sub-manifest
		platforms := make([]types.Platform, 0, len(indexManifest.Manifests))
		for _, manifest := range indexManifest.Manifests {
			if manifest.Platform == nil {
				continue
			}

			platforms = append(platforms, types.Platform{
				OS:      manifest.Platform.OS,
				Arch:    manifest.Platform.Architecture,
				Variant: manifest.Platform.Variant,
				Digest:  manifest.Digest.String(),
			})
		}

		return platforms, nil
	}

	// It's a single-arch image, return its platform
	img, err := desc.Image()
	if err != nil {
		return nil, fmt.Errorf("getting image: %w", err)
	}

	configFile, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("getting config file: %w", err)
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("getting image digest: %w", err)
	}

	return []types.Platform{{
		OS:      configFile.OS,
		Arch:    configFile.Architecture,
		Variant: configFile.Variant,
		Digest:  digest.String(),
	}}, nil
}

// discoverReportsInDirectory scans a directory for vulnerability reports
func discoverReportsInDirectory(reportDir string) (map[string]string, error) {
	if reportDir == "" {
		return nil, fmt.Errorf("report directory not specified")
	}

	// Check if directory exists
	info, err := os.Stat(reportDir)
	if err != nil {
		return nil, fmt.Errorf("accessing report directory: %w", err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", reportDir)
	}

	// Scan directory for report files
	entries, err := os.ReadDir(reportDir)
	if err != nil {
		return nil, fmt.Errorf("reading report directory: %w", err)
	}

	// Map to store platform -> report file path
	reports := make(map[string]string)

	// Look for files with patterns like report-linux-amd64.json, etc.
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filename := entry.Name()
		// Look for patterns in filenames to extract platform info
		// This is a simple example; in practice, more robust parsing may be needed

		// Try to match patterns like report-<os>-<arch>.json or similar
		matches := reportFileRegex.FindStringSubmatch(filename)
		if len(matches) >= 3 {
			os := matches[1]
			arch := matches[2]
			platformKey := fmt.Sprintf("%s/%s", os, arch)
			reports[platformKey] = filepath.Join(reportDir, filename)
		}
	}

	return reports, nil
}

// findPatchablePlatforms identifies which platforms have both a manifest and a report
func findPatchablePlatforms(subManifests []types.Platform, availableReports map[string]string) []types.Platform {
	var patchablePlatforms []types.Platform

	for _, platform := range subManifests {
		platformKey := fmt.Sprintf("%s/%s", platform.OS, platform.Arch)
		if reportPath, exists := availableReports[platformKey]; exists {
			// This platform has both a manifest and a report
			platform.ReportPath = reportPath
			patchablePlatforms = append(patchablePlatforms, platform)
		}
	}

	return patchablePlatforms
}

// GetAllPlatforms returns all platforms available in the image
func GetAllPlatforms(imageRef string) ([]types.Platform, error) {
	return discoverImagePlatforms(imageRef)
}
