package generate

import (
	"context"
	"time"
)

// Generate creates a tar stream containing a Dockerfile and patch layer
func Generate(ctx context.Context, timeout time.Duration, image, reportFile, patchedTag, suffix, workingFolder, scanner string, ignoreErrors bool, outputPath string) error {
	// For now, use the simple implementation that doesn't require BuildKit
	// TODO: In the future, we can add a flag to choose between simple and full BuildKit-based implementation
	return GenerateSimple(ctx, timeout, image, reportFile, patchedTag, suffix, workingFolder, scanner, ignoreErrors, outputPath)
}