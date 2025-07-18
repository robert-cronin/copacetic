package generate

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/project-copacetic/copacetic/pkg/report"
	"github.com/project-copacetic/copacetic/pkg/types/unversioned"
)

const (
	defaultTag = "latest"
)

// patchLayerData contains the files and metadata for the patch layer
type patchLayerData struct {
	files       map[string][]byte // path -> content
	permissions map[string]int    // path -> mode
	commands    []string          // Commands that were run
}

// GenerateSimple creates a tar stream without requiring BuildKit
// This is a simplified implementation that demonstrates the concept
func GenerateSimple(ctx context.Context, timeout time.Duration, image, reportFile, patchedTag, suffix, workingFolder, scanner string, ignoreErrors bool, outputPath string) error {
	// Set timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ch := make(chan error)
	go func() {
		ch <- generateSimpleWithContext(timeoutCtx, image, reportFile, patchedTag, suffix, workingFolder, scanner, ignoreErrors, outputPath)
	}()

	select {
	case err := <-ch:
		return err
	case <-timeoutCtx.Done():
		<-time.After(1 * time.Second)
		err := fmt.Errorf("generate exceeded timeout %v", timeout)
		log.Error(err)
		return err
	}
}

func generateSimpleWithContext(ctx context.Context, image, reportPath, patchedTag, suffix, workingFolder, scanner string, ignoreErrors bool, outputPath string) error {
	// Parse image reference
	imageName, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return fmt.Errorf("failed to parse reference: %w", err)
	}

	// Resolve patched tag
	patchedTag, err = resolvePatchedTag(imageName, patchedTag, suffix)
	if err != nil {
		return err
	}
	
	patchedImageName := fmt.Sprintf("%s:%s", imageName.Name(), patchedTag)
	log.Infof("Patched image name: %s", patchedImageName)

	// Parse vulnerability report if provided
	var updates *unversioned.UpdateManifest
	if reportPath != "" {
		updates, err = report.TryParseScanReport(reportPath, scanner)
		if err != nil {
			return err
		}
		log.Debugf("updates to apply: %v", updates)
	}

	// Normalize image reference
	var ref string
	if reference.IsNameOnly(imageName) {
		log.Warnf("Image name has no tag or digest, using latest as tag")
		ref = fmt.Sprintf("%s:%s", imageName.Name(), defaultTag)
	} else {
		ref = imageName.String()
	}

	// Create patch data based on the vulnerability report
	patchData := createPatchDataFromReport(updates)

	// Create tar stream
	return createSimpleTarStream(ref, patchedTag, patchData, outputPath)
}

// createPatchDataFromReport creates patch data based on vulnerability report
// In a real implementation, this would contain actual patch files
func createPatchDataFromReport(updates *unversioned.UpdateManifest) *patchLayerData {
	patchData := &patchLayerData{
		files:       make(map[string][]byte),
		permissions: make(map[string]int),
		commands:    []string{},
	}

	if updates == nil || len(updates.Updates) == 0 {
		log.Info("No packages to update")
		return patchData
	}

	// Create a manifest file documenting what would be patched
	var manifestContent bytes.Buffer
	manifestContent.WriteString("Copa Patch Manifest\n")
	manifestContent.WriteString("===================\n\n")
	manifestContent.WriteString(fmt.Sprintf("Generated: %s\n", time.Now().UTC().Format(time.RFC3339)))
	manifestContent.WriteString(fmt.Sprintf("OS: %s %s\n", updates.Metadata.OS.Type, updates.Metadata.OS.Version))
	manifestContent.WriteString(fmt.Sprintf("Architecture: %s\n\n", updates.Metadata.Config.Arch))
	manifestContent.WriteString("Packages to update:\n")

	for _, update := range updates.Updates {
		manifestContent.WriteString(fmt.Sprintf("- %s: %s -> %s (fixes %s)\n", 
			update.Name, 
			update.InstalledVersion, 
			update.FixedVersion,
			update.VulnerabilityID))
	}

	// Store the manifest file
	patchData.files["/copa/patch-manifest.txt"] = manifestContent.Bytes()
	patchData.permissions["/copa/patch-manifest.txt"] = 0644

	// Add commands that would be run
	switch updates.Metadata.OS.Type {
	case "debian", "ubuntu":
		patchData.commands = []string{
			"apt-get update",
			fmt.Sprintf("apt-get install -y %s", getPackageList(updates)),
		}
	case "alpine":
		patchData.commands = []string{
			"apk update", 
			fmt.Sprintf("apk add --no-cache %s", getPackageList(updates)),
		}
	case "centos", "rhel", "fedora":
		patchData.commands = []string{
			fmt.Sprintf("yum install -y %s", getPackageList(updates)),
		}
	}

	return patchData
}

func getPackageList(updates *unversioned.UpdateManifest) string {
	var packages []string
	for _, update := range updates.Updates {
		packages = append(packages, fmt.Sprintf("%s=%s", update.Name, update.FixedVersion))
	}
	return strings.Join(packages, " ")
}

// createSimpleTarStream creates the tar stream with Dockerfile and patch layer
func createSimpleTarStream(image, patchedTag string, patchData *patchLayerData, outputPath string) error {
	// Open output writer
	var w io.Writer = os.Stdout
	if outputPath != "" {
		f, err := os.Create(outputPath)
		if err != nil {
			return errors.Wrap(err, "failed to create output file")
		}
		defer f.Close()
		w = f
	}

	tw := tar.NewWriter(w)
	defer tw.Close()

	// Generate Dockerfile
	dockerfile := fmt.Sprintf(`FROM %s
COPY patch/ /
LABEL org.opencontainers.image.revision="%s"
`, image, patchedTag)

	// Add commands as comments for documentation
	if len(patchData.commands) > 0 {
		dockerfile += "\n# Patch commands that would be applied:\n"
		for _, cmd := range patchData.commands {
			dockerfile += fmt.Sprintf("# RUN %s\n", cmd)
		}
	}

	// Add metadata labels
	dockerfile += fmt.Sprintf(`
LABEL sh.copa.patch.timestamp="%s"
LABEL sh.copa.patch.mode="generate"
`, time.Now().UTC().Format(time.RFC3339))

	// Write Dockerfile
	dockerfileHeader := &tar.Header{
		Name:    "Dockerfile",
		Mode:    0644,
		Size:    int64(len(dockerfile)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(dockerfileHeader); err != nil {
		return errors.Wrap(err, "failed to write Dockerfile header")
	}
	if _, err := tw.Write([]byte(dockerfile)); err != nil {
		return errors.Wrap(err, "failed to write Dockerfile content")
	}

	// Create patch directory structure
	writtenDirs := make(map[string]bool)
	
	// Write patch files
	for filePath, content := range patchData.files {
		// Ensure the path is under patch/
		tarPath := filepath.Join("patch", strings.TrimPrefix(filePath, "/"))
		
		// Create parent directories
		dir := filepath.Dir(tarPath)
		for dir != "." && dir != "/" && dir != "patch" && !writtenDirs[dir] {
			dirHeader := &tar.Header{
				Name:     dir + "/",
				Mode:     0755,
				Typeflag: tar.TypeDir,
				ModTime:  time.Now(),
			}
			if err := tw.WriteHeader(dirHeader); err != nil {
				return errors.Wrapf(err, "failed to write directory header for %s", dir)
			}
			writtenDirs[dir] = true
			dir = filepath.Dir(dir)
		}

		// Write file
		mode := patchData.permissions[filePath]
		if mode == 0 {
			mode = 0644
		}
		
		fileHeader := &tar.Header{
			Name:    tarPath,
			Mode:    int64(mode),
			Size:    int64(len(content)),
			ModTime: time.Now(),
		}
		if err := tw.WriteHeader(fileHeader); err != nil {
			return errors.Wrapf(err, "failed to write file header for %s", filePath)
		}
		if _, err := tw.Write(content); err != nil {
			return errors.Wrapf(err, "failed to write file content for %s", filePath)
		}
	}

	// If no files were patched, create an empty marker
	if len(patchData.files) == 0 {
		// Create patch directory
		dirHeader := &tar.Header{
			Name:     "patch/",
			Mode:     0755,
			Typeflag: tar.TypeDir,
			ModTime:  time.Now(),
		}
		if err := tw.WriteHeader(dirHeader); err != nil {
			return errors.Wrap(err, "failed to write patch directory header")
		}

		// Write empty marker
		emptyMarker := "patch/.copa-no-updates"
		emptyContent := []byte("No security updates required\n")
		fileHeader := &tar.Header{
			Name:    emptyMarker,
			Mode:    0644,
			Size:    int64(len(emptyContent)),
			ModTime: time.Now(),
		}
		if err := tw.WriteHeader(fileHeader); err != nil {
			return errors.Wrap(err, "failed to write empty marker header")
		}
		if _, err := tw.Write(emptyContent); err != nil {
			return errors.Wrap(err, "failed to write empty marker content")
		}
	}

	// Ensure proper tar termination
	if err := tw.Flush(); err != nil {
		return errors.Wrap(err, "failed to flush tar writer")
	}

	log.Info("Successfully generated Docker build context")
	return nil
}

// resolvePatchedTag merges explicit tag & suffix rules (copied from patch.go)
func resolvePatchedTag(imageRef reference.Named, explicitTag, suffix string) (string, error) {
	if explicitTag != "" {
		return explicitTag, nil
	}

	var baseTag string
	if tagged, ok := imageRef.(reference.Tagged); ok {
		baseTag = tagged.Tag()
	}

	if suffix == "" {
		suffix = "patched"
	}

	if baseTag == "" {
		return "", fmt.Errorf("no tag found in image reference %s", imageRef.String())
	}

	return fmt.Sprintf("%s-%s", baseTag, suffix), nil
}