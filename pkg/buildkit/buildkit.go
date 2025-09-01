package buildkit

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	ispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/project-copacetic/copacetic/pkg/report"
	"github.com/project-copacetic/copacetic/pkg/types"
	log "github.com/sirupsen/logrus"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	v1types "github.com/google/go-containerregistry/pkg/v1/types"
)

type Config struct {
	ImageName         string
	Client            gwclient.Client
	ConfigData        []byte
	PatchedConfigData []byte
	Platform          *ispec.Platform
	ImageState        llb.State
	PatchedImageState llb.State
}

type Opts struct {
	Addr       string
	CACertPath string
	CertPath   string
	KeyPath    string
}

const (
	linux = "linux"
	arm64 = "arm64"
)

// for testing.
var (
	readDir  = os.ReadDir
	readFile = os.ReadFile
	lookPath = exec.LookPath
)

func InitializeBuildkitConfig(
	ctx context.Context,
	c gwclient.Client,
	userImage string,
	platform *ispec.Platform,
) (*Config, error) {
	// Initialize buildkit config for the target image
	config := Config{
		ImageName: userImage,
		Platform:  platform,
	}

	// Resolve and pull the config for the target image
	resolveOpt := sourceresolver.Opt{
		ImageOpt: &sourceresolver.ResolveImageOpt{
			ResolveMode: llb.ResolveModePreferLocal.String(),
		},
	}
	if platform != nil {
		resolveOpt.Platform = platform
	}
	_, _, configData, err := c.ResolveImageConfig(ctx, userImage, resolveOpt)
	if err != nil {
		return nil, err
	}

	var baseImage string
	config.ConfigData, config.PatchedConfigData, baseImage, err = updateImageConfigData(ctx, c, configData, userImage)
	if err != nil {
		return nil, err
	}

	// Load the target image state with the resolved image config in case environment variable settings
	// are necessary for running apps in the target image for updates
	imageOpts := []llb.ImageOption{
		llb.ResolveModePreferLocal,
		llb.WithMetaResolver(c),
	}
	if platform != nil {
		imageOpts = append(imageOpts, llb.Platform(*platform))
	}
	config.ImageState, err = llb.Image(baseImage, imageOpts...).WithImageConfig(config.ConfigData)
	if err != nil {
		return nil, err
	}

	// Only set PatchedImageState if the user supplied a patched image
	// An image is deemed to be a patched image if it contains one of two metadata values
	// BaseImage or ispec.AnnotationBaseImageName
	if config.PatchedConfigData != nil {
		patchedImageOpts := []llb.ImageOption{
			llb.ResolveModePreferLocal,
			llb.WithMetaResolver(c),
		}
		if platform != nil {
			patchedImageOpts = append(patchedImageOpts, llb.Platform(*platform))
		}
		config.PatchedImageState, err = llb.Image(userImage, patchedImageOpts...).WithImageConfig(config.PatchedConfigData)
		if err != nil {
			return nil, err
		}
	}

	config.Client = c

	return &config, nil
}

func DiscoverPlatformsFromReport(reportDir, scanner string) ([]types.PatchPlatform, error) {
	var platforms []types.PatchPlatform

	reportNames, err := os.ReadDir(reportDir)
	if err != nil {
		return nil, err
	}

	for _, file := range reportNames {
		filePath := reportDir + "/" + file.Name()
		if file.IsDir() {
			continue
		}
		report, err := report.TryParseScanReport(filePath, scanner)
		if err != nil {
			return nil, fmt.Errorf("error parsing report %w", err)
		}

		// use this to confirm that os type (ex/Debian) is linux based and supported since report.Metadata.OS.Type gives specific like "debian" rather than "linux"
		if !isSupportedOsType(report.Metadata.OS.Type) {
			continue
		}

		platform := types.PatchPlatform{
			Platform: ispec.Platform{
				OS:           linux,
				Architecture: report.Metadata.Config.Arch,
				Variant:      report.Metadata.Config.Variant,
			},
			ReportFile:     filePath,
			ShouldPreserve: false, // This platform has a report, so it should be patched
		}

		if platform.Architecture == arm64 && platform.Variant == "v8" {
			// removing this to maintain consistency since we do
			// the same for the platforms discovered from reports
			platform.Variant = ""
		}
		platforms = append(platforms, platform)
	}

	return platforms, nil
}

func isSupportedOsType(osType string) bool {
	switch osType {
	case "alpine", "debian", "ubuntu", "cbl-mariner", "azurelinux", "centos", "oracle", "redhat", "rocky", "amazon", "alma":
		return true
	default:
		return false
	}
}

// tryGetManifestFromLocal attempts to get manifest data from the local Docker daemon.
func tryGetManifestFromLocal(ref name.Reference) (*remote.Descriptor, error) {
	imageName := ref.String()
	log.Debugf("Trying: docker manifest inspect %s", imageName)

	cmd := exec.Command("docker", "manifest", "inspect", imageName)
	output, err := cmd.Output()
	if err != nil {
		log.Debugf("docker manifest inspect failed for %s: %v", imageName, err)
		return nil, fmt.Errorf("failed to inspect manifest using docker CLI: %v", err)
	}

	// Parse the manifest data
	var manifestData map[string]interface{}
	if err := json.Unmarshal(output, &manifestData); err != nil {
		log.Debugf("Failed to parse manifest JSON for %s: %v", imageName, err)
		return nil, fmt.Errorf("failed to parse manifest JSON: %v", err)
	}

	// Check if this is a manifest list (has "manifests" field)
	if manifests, ok := manifestData["manifests"]; ok {
		if manifestSlice, ok := manifests.([]interface{}); ok && len(manifestSlice) > 0 {
			log.Debugf("Found multi-platform manifest via CLI with %d platforms", len(manifestSlice))

			// Parse the manifest list to extract individual platform image references
			var enhancedManifestData struct {
				MediaType string `json:"mediaType"`
				Manifests []struct {
					Digest    string `json:"digest"`
					MediaType string `json:"mediaType"`
					Size      int64  `json:"size"`
					Platform  struct {
						Architecture string `json:"architecture"`
						OS           string `json:"os"`
						Variant      string `json:"variant,omitempty"`
					} `json:"platform"`
				} `json:"manifests"`
			}

			if err := json.Unmarshal(output, &enhancedManifestData); err != nil {
				log.Debugf("Failed to parse enhanced manifest JSON for %s: %v", imageName, err)
				return nil, fmt.Errorf("failed to parse enhanced manifest JSON: %v", err)
			}

			// Log platform information for debugging
			log.Debugf("Manifest list contains the following platforms:")
			for i, manifest := range enhancedManifestData.Manifests {
				log.Debugf("  Platform %d: %s/%s (digest: %s)", i+1,
					manifest.Platform.OS, manifest.Platform.Architecture,
					manifest.Digest[:12]+"...")
			}

			// Determine media type
			mediaType := "application/vnd.docker.distribution.manifest.list.v2+json"
			if enhancedManifestData.MediaType != "" {
				mediaType = enhancedManifestData.MediaType
			}

			// Calculate digest from the manifest content
			digest := fmt.Sprintf("%x", sha256.Sum256(output))

			return &remote.Descriptor{
				Descriptor: v1.Descriptor{
					MediaType: v1types.MediaType(mediaType),
					Size:      int64(len(output)),
					Digest:    v1.Hash{Algorithm: "sha256", Hex: digest},
				},
				Manifest: output,
			}, nil
		}
	}

	return nil, fmt.Errorf("single-platform image")
}

// DiscoverPlatformsFromReference discovers platforms from both local and remote manifests.
// It first attempts to inspect the manifest locally using Docker API
// to get raw manifest data and determine if it's multi-platform.
// If local inspection fails, it falls back to remote registry inspection.
// This allows Copa to patch multi-platform manifests that exist locally but not in the registry.
func DiscoverPlatformsFromReference(manifestRef string) ([]types.PatchPlatform, error) {
	var platforms []types.PatchPlatform

	ref, err := name.ParseReference(manifestRef)
	if err != nil {
		return nil, fmt.Errorf("error parsing reference %q: %w", manifestRef, err)
	}

	// Try local daemon first, then fall back to remote
	desc, err := tryGetManifestFromLocal(ref)
	if err != nil {
		log.Debugf("Failed to get descriptor from local daemon: %v, trying remote registry", err)
		desc, err = remote.Get(ref)
		if err != nil {
			return nil, fmt.Errorf("error fetching descriptor for %q from both local daemon and remote registry: %w", manifestRef, err)
		}
		log.Debugf("Successfully fetched descriptor from remote registry for %s", manifestRef)
	} else {
		log.Debugf("Successfully fetched descriptor from local daemon for %s", manifestRef)
	}

	if desc.MediaType.IsIndex() {
		index, err := desc.ImageIndex()
		if err != nil {
			return nil, fmt.Errorf("error getting image index %w", err)
		}

		manifest, err := index.IndexManifest()
		if err != nil {
			return nil, fmt.Errorf("error getting manifest: %w", err)
		}

		for i := range manifest.Manifests {
			m := &manifest.Manifests[i]

			// Skip manifests with unknown platforms
			if m.Platform == nil || m.Platform.OS == "unknown" || m.Platform.Architecture == "unknown" {
				log.Debugf("Skipping manifest with unknown platform: %s/%s", m.Platform.OS, m.Platform.Architecture)
				continue
			}

			patchPlatform := types.PatchPlatform{
				Platform: ispec.Platform{
					OS:           m.Platform.OS,
					Architecture: m.Platform.Architecture,
					Variant:      m.Platform.Variant,
					OSVersion:    m.Platform.OSVersion,
					OSFeatures:   m.Platform.OSFeatures,
				},
				ReportFile:     "",    // No report file for platforms discovered from reference
				ShouldPreserve: false, // Default to false, will be set appropriately later
			}
			if m.Platform.Architecture == arm64 && m.Platform.Variant == "v8" {
				// some scanners may not add v8 to arm64 reports, so we
				// need to remove it here to maintain consistency
				patchPlatform.Variant = ""
			}
			platforms = append(platforms, patchPlatform)
		}
		return platforms, nil
	}

	// For single-platform images, try to get the image config to extract platform information
	img, err := desc.Image()
	if err != nil {
		return nil, fmt.Errorf("error getting image %w", err)
	}

	config, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("error getting image config %w", err)
	}

	// Extract platform from image config
	if config.Architecture != "" && config.OS != "" {
		platform := types.PatchPlatform{
			Platform: ispec.Platform{
				OS:           config.OS,
				Architecture: config.Architecture,
				Variant:      config.Variant,
			},
			ReportFile:     "",
			ShouldPreserve: false,
		}
		if platform.Architecture == arm64 && platform.Variant == "v8" {
			platform.Variant = ""
		}
		return []types.PatchPlatform{platform}, nil
	}

	// return nil if platform information is not available
	return nil, nil
}

//nolint:gocritic
func PlatformKey(pl ispec.Platform) string {
	// if platform is present in list from reference and report, then we should patch that platform
	key := pl.OS + "/" + pl.Architecture
	if pl.Variant != "" {
		key += "/" + pl.Variant
	}
	// Include OS version for platforms like Windows that have multiple versions
	if pl.OSVersion != "" {
		key += "@" + pl.OSVersion
	}
	return key
}

func DiscoverPlatforms(manifestRef, reportDir, scanner string) ([]types.PatchPlatform, error) {
	var platforms []types.PatchPlatform

	p, err := DiscoverPlatformsFromReference(manifestRef)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("image is not multi platform")
	}
	log.WithField("platforms", p).Debug("Discovered platforms from manifest")

	if reportDir != "" {
		p2, err := DiscoverPlatformsFromReport(reportDir, scanner)
		if err != nil {
			return nil, err
		}
		log.WithField("platforms", p2).Debug("Discovered platforms from report")

		// include all platforms from original manifest, patching only those with reports
		reportSet := make(map[string]string, len(p2))
		for _, pl := range p2 {
			reportSet[PlatformKey(pl.Platform)] = pl.ReportFile
		}

		for _, pl := range p {
			if rp, ok := reportSet[PlatformKey(pl.Platform)]; ok {
				// Platform has a report - will be patched
				pl.ReportFile = rp
				pl.ShouldPreserve = false
				platforms = append(platforms, pl)
			} else {
				// Platform has no report - preserve original without patching
				log.Debugf("No report found for platform %s, preserving original", PlatformKey(pl.Platform))
				pl.ReportFile = ""
				pl.ShouldPreserve = true
				platforms = append(platforms, pl)
			}
		}

		return platforms, nil
	}

	return p, nil
}

// GetPlatformImageReference resolves a platform-specific image reference from a local manifest.
// For multi-platform images that exist locally but not in the registry, this function extracts
// the platform-specific digest and constructs a reference that BuildKit can resolve.
func GetPlatformImageReference(manifestRef string, targetPlatform *ispec.Platform) (string, error) {
	ref, err := name.ParseReference(manifestRef)
	if err != nil {
		return "", fmt.Errorf("error parsing reference %q: %w", manifestRef, err)
	}

	// Try to get the local manifest first
	desc, err := tryGetManifestFromLocal(ref)
	if err != nil {
		// Not a local manifest, return original reference
		return manifestRef, nil
	}

	if !desc.MediaType.IsIndex() {
		// Single platform image, return original reference
		return manifestRef, nil
	}

	// Parse the manifest to extract platform-specific information
	var manifestData struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
				Variant      string `json:"variant,omitempty"`
			} `json:"platform"`
		} `json:"manifests"`
	}

	if err := json.Unmarshal(desc.Manifest, &manifestData); err != nil {
		return "", fmt.Errorf("failed to parse manifest JSON: %w", err)
	}

	// Find the matching platform
	for _, manifest := range manifestData.Manifests {
		manifestPlatform := manifest.Platform

		// Normalize arm64 variant for comparison
		if manifestPlatform.Architecture == "arm64" && manifestPlatform.Variant == "v8" {
			manifestPlatform.Variant = ""
		}
		targetVariant := targetPlatform.Variant
		if targetPlatform.Architecture == "arm64" && targetVariant == "v8" {
			targetVariant = ""
		}

		// Check if platforms match
		if manifestPlatform.OS == targetPlatform.OS &&
			manifestPlatform.Architecture == targetPlatform.Architecture &&
			manifestPlatform.Variant == targetVariant {

			// For local manifests, we need to construct a reference to the platform-specific image
			// Extract the base repository name (without tag/digest)
			baseRepo := ref.Context().Name()

			// Construct platform-specific image reference with digest
			platformImageRef := baseRepo + "@" + manifest.Digest

			log.Debugf("Found platform %s/%s in local manifest, using image reference: %s",
				manifestPlatform.OS, manifestPlatform.Architecture, platformImageRef)
			return platformImageRef, nil
		}
	}

	return "", fmt.Errorf("platform %s/%s not found in manifest", targetPlatform.OS, targetPlatform.Architecture)
}

func updateImageConfigData(ctx context.Context, c gwclient.Client, configData []byte, image string) ([]byte, []byte, string, error) {
	baseImage, userImageConfig, err := setupLabels(image, configData)
	if err != nil {
		return nil, nil, "", err
	}

	if baseImage == "" {
		configData = userImageConfig
	} else {
		patchedImageConfig := userImageConfig
		_, _, baseImageConfig, err := c.ResolveImageConfig(ctx, baseImage, sourceresolver.Opt{
			ImageOpt: &sourceresolver.ResolveImageOpt{
				ResolveMode: llb.ResolveModePreferLocal.String(),
			},
		})
		if err != nil {
			return nil, nil, "", err
		}

		_, baseImageWithLabels, _ := setupLabels(baseImage, baseImageConfig)
		configData = baseImageWithLabels

		return configData, patchedImageConfig, baseImage, nil
	}

	return configData, nil, image, nil
}

func setupLabels(image string, configData []byte) (string, []byte, error) {
	imageConfig := make(map[string]interface{})
	err := json.Unmarshal(configData, &imageConfig)
	if err != nil {
		return "", nil, err
	}

	configMap, ok := imageConfig["config"].(map[string]interface{})
	if !ok {
		err := fmt.Errorf("type assertion to map[string]interface{} failed")
		return "", nil, err
	}

	var baseImage string
	labels := configMap["labels"]
	if labels == nil {
		configMap["labels"] = make(map[string]interface{})
	}
	labelsMap, ok := configMap["labels"].(map[string]interface{})
	if !ok {
		err := fmt.Errorf("type assertion to map[string]interface{} failed")
		return "", nil, err
	}
	if baseImageValue := labelsMap["BaseImage"]; baseImageValue != nil {
		baseImage, ok = baseImageValue.(string)
		if !ok {
			err := fmt.Errorf("type assertion to string failed")
			return "", nil, err
		}
	} else {
		labelsMap["BaseImage"] = image
	}

	imageWithLabels, _ := json.Marshal(imageConfig)

	return baseImage, imageWithLabels, nil
}

// Extracts the bytes of the file denoted by `path` from the state `st`.
func ExtractFileFromState(ctx context.Context, c gwclient.Client, st *llb.State, path string) ([]byte, error) {
	// since platform is obtained from host, override it in the case of Darwin
	platform := platforms.Normalize(platforms.DefaultSpec())
	if platform.OS != linux {
		platform.OS = linux
	}

	def, err := st.Marshal(ctx, llb.Platform(platform))
	if err != nil {
		return nil, err
	}

	resp, err := c.Solve(ctx, gwclient.SolveRequest{
		Evaluate:   true,
		Definition: def.ToPB(),
	})
	if err != nil {
		return nil, err
	}

	ref, err := resp.SingleRef()
	if err != nil {
		return nil, err
	}

	return ref.ReadFile(ctx, gwclient.ReadRequest{
		Filename: path,
	})
}

func Sh(cmd string) llb.RunOption {
	return llb.Args([]string{"/bin/sh", "-c", cmd})
}

func ArrayFile(input []string) []byte {
	var b bytes.Buffer
	for _, s := range input {
		b.WriteString(s)
		b.WriteRune('\n') // newline
	}
	return b.Bytes()
}

func WithArrayFile(s *llb.State, path string, contents []string) llb.State {
	af := ArrayFile(contents)
	return WithFileBytes(s, path, af)
}

func WithFileString(s *llb.State, path, contents string) llb.State {
	return WithFileBytes(s, path, []byte(contents))
}

func WithFileBytes(s *llb.State, path string, contents []byte) llb.State {
	return s.File(llb.Mkfile(path, 0o600, contents))
}

func QemuAvailable(p *types.PatchPlatform) bool {
	if p == nil {
		return false
	}

	// check if were on macos or windows
	switch runtime.GOOS {
	case "darwin":
		// on macos, we cant directly check binfmt_misc on the host
		// we assume docker desktop handles emulation
		log.Warn("Running on macOS, assuming Docker Desktop handles emulation.")
		return true
	case "windows":
		log.Warn("Running on Windows, assuming Docker Desktop handles emulation.")
		return true
	}

	archKey := mapGoArch(p.Architecture, p.Variant)

	// walk binfmt_misc entries
	entries, err := readDir("/proc/sys/fs/binfmt_misc")
	if err != nil {
		return false
	}

	for _, e := range entries {
		if e.IsDir() || e.Name() == "register" || e.Name() == "status" {
			continue
		}
		data, _ := readFile("/proc/sys/fs/binfmt_misc/" + e.Name())
		if bytes.Contains(data, []byte("interpreter")) &&
			bytes.Contains(data, []byte("qemu-"+archKey)) {
			return true
		}
	}
	// fallback to interpreter binary on PATH (for rootless case)
	if _, err := lookPath("qemu-" + archKey + "-static"); err == nil {
		return true
	}
	return false
}

func mapGoArch(arch, variant string) string {
	switch arch {
	case "amd64", "amd64p32":
		return "x86_64"

	case "386":
		return "i386"

	case "arm64", "arm64be":
		return "aarch64"

	case "arm":
		// GOARM=5/6/7 -> qemu-arm
		// big-endian -> qemu-armeb
		if strings.HasSuffix(variant, "eb") || strings.HasSuffix(arch, "be") {
			return "armeb"
		}
		return "arm"

	case "mips":
		if strings.HasSuffix(arch, "le") {
			return "mipsel"
		}
		return "mips"

	case "mips64":
		if strings.HasSuffix(variant, "n32") {
			return "mipsn32"
		}
		if strings.HasSuffix(arch, "le") {
			return "mips64el"
		}
		return "mips64"

	case "mips64le":
		if strings.HasSuffix(variant, "n32") {
			return "mipsn32el"
		}
		return "mips64el"

	case "ppc64":
		if strings.HasSuffix(variant, "le") {
			return "ppc64le"
		}
		return "ppc64"

	case "loong64":
		return "loongarch64"

	case "sh4":
		if strings.HasSuffix(variant, "eb") {
			return "sh4eb"
		}
		return "sh4"

	case "xtensa":
		if strings.HasSuffix(variant, "eb") {
			return "xtensaeb"
		}
		return "xtensa"

	case "microblaze":
		if strings.HasSuffix(variant, "el") {
			return "microblazeel"
		}
		return "microblaze"
	}

	// fallback: hope QEMU name == GOARCH
	return arch
}

// CreateOCILayoutFromResults creates an OCI layout directory from patch results using BuildKit's OCI exporter.
func CreateOCILayoutFromResults(outputDir string, results []types.PatchResult, platforms []types.PatchPlatform) error {
	log.Infof("Creating multi-platform OCI layout in directory: %s with %d platforms", outputDir, len(platforms))

	// Create output directory
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Map platforms to their corresponding images
	platformImages := make(map[string]string)

	// Process results to build platform->image mapping
	for _, result := range results {
		// Determine which platform this result belongs to
		for _, platform := range platforms {
			platformKey := PlatformKey(platform.Platform)

			// Check if this result matches this platform
			if result.PatchedRef.String() != result.OriginalRef.String() {
				// This is a patched image - check if it has the platform suffix
				expectedSuffix := getPlatformSuffix(&platform.Platform)
				if strings.HasSuffix(result.PatchedRef.String(), expectedSuffix) {
					platformImages[platformKey] = result.PatchedRef.String()
					log.Debugf("Platform %s: using patched image %s", platformKey, result.PatchedRef.String())
					break
				}
			} else {
				// This is a preserved image - assign it to unassigned platforms
				if _, exists := platformImages[platformKey]; !exists {
					platformImages[platformKey] = result.OriginalRef.String()
					log.Debugf("Platform %s: using preserved image %s", platformKey, result.OriginalRef.String())
				}
			}
		}
	}

	if len(platformImages) == 0 {
		return fmt.Errorf("no platform images found")
	}

	log.Infof("Found %d platform images, creating BuildKit-based OCI layout", len(platformImages))

	// Use docker buildx to create proper multi-platform OCI layout
	return createMultiPlatformOCIWithBuildx(outputDir, platformImages, platforms)
}

// createMultiPlatformOCIWithBuildx uses BuildKit Go client directly to create a proper multi-platform OCI layout from local patched images.
func createMultiPlatformOCIWithBuildx(outputDir string, platformImages map[string]string, platforms []types.PatchPlatform) error {
	if len(platformImages) == 0 {
		return fmt.Errorf("no platform images to build")
	}

	log.Infof("Creating multiplatform manifest from %d platform images using BuildKit Go client", len(platformImages))

	// Use BuildKit Go client directly to create OCI layout
	ctx := context.Background()
	
	// Try to create the OCI layout using BuildKit's OCI exporter directly
	return createOCILayoutWithBuildKitClient(ctx, outputDir, platformImages, platforms)
}

// createOCILayoutWithBuildKitClient uses BuildKit Go client to create OCI layout from local images
func createOCILayoutWithBuildKitClient(ctx context.Context, outputDir string, platformImages map[string]string, platforms []types.PatchPlatform) error {
	log.Infof("Using BuildKit Go client libraries to create OCI layout")

	// Use Copa's existing BuildKit client factory to get the proper client
	bkOpts := Opts{} // Use default options - this will auto-detect the best BuildKit driver
	c, err := NewClient(ctx, bkOpts)
	if err != nil {
		return fmt.Errorf("failed to create BuildKit client: %w", err)
	}
	defer c.Close()
	
	// Create LLB states for each platform image
	var platformStates []llb.State
	var platformSpecs []specs.Platform
	
	for _, platform := range platforms {
		platformKey := PlatformKey(platform.Platform)
		if imageRef, exists := platformImages[platformKey]; exists {
			// Create LLB state from the local image
			// Use ResolveModePreferLocal to try local first
			platformSpec := specs.Platform{
				OS:           platform.Platform.OS,
				Architecture: platform.Platform.Architecture,
				Variant:      platform.Platform.Variant,
			}
			
			// Strip docker.io prefix for local resolution
			localImageRef := strings.TrimPrefix(imageRef, "docker.io/")
			
			log.Debugf("Creating LLB state for platform %s from image %s", platformKey, localImageRef)
			
			imageOpts := []llb.ImageOption{
				llb.ResolveModePreferLocal,
				llb.Platform(platformSpec),
			}
			
			state := llb.Image(localImageRef, imageOpts...)
			platformStates = append(platformStates, state)
			platformSpecs = append(platformSpecs, platformSpec)
		}
	}
	
	if len(platformStates) == 0 {
		return fmt.Errorf("no platform states created")
	}
	
	log.Infof("Created %d platform states, solving with BuildKit", len(platformStates))
	
	// Use BuildKit to solve the multi-platform build and export to OCI
	return solveMultiPlatformOCI(ctx, c, outputDir, platformStates, platformSpecs)
}

// solveMultiPlatformOCI uses BuildKit client to solve multi-platform states and export to OCI layout
func solveMultiPlatformOCI(ctx context.Context, c *client.Client, outputDir string, platformStates []llb.State, platformSpecs []specs.Platform) error {
	log.Infof("Solving %d platform states with BuildKit and exporting to OCI layout", len(platformStates))
	
	if len(platformStates) == 0 {
		return fmt.Errorf("no platform states provided")
	}
	
	// Remove output directory if it exists
	os.RemoveAll(outputDir)
	
	// Create solve options for OCI export using Output function instead of OutputDir
	// This avoids the diffcopy method issue by providing a writer interface
	solveOpt := client.SolveOpt{
		Exports: []client.ExportEntry{{
			Type: client.ExporterOCI,
			Attrs: map[string]string{
				"oci-mediatypes": "true",
			},
			Output: func(map[string]string) (io.WriteCloser, error) {
				// Create a tar file that we'll extract to the OCI layout
				tarFile, err := os.Create(outputDir + ".tar")
				if err != nil {
					return nil, fmt.Errorf("failed to create OCI tar file: %w", err)
				}
				return tarFile, nil
			},
		}},
	}
	
	// Use Build() with gateway client callback pattern (same as Copa's existing usage)
	buildChannel := make(chan *client.SolveStatus)
	// Start a goroutine to consume status updates (we don't need to display them for OCI export)
	go func() {
		for range buildChannel {
			// Consume status updates silently
		}
	}()
	
	_, err := c.Build(ctx, solveOpt, "copa-oci-export", func(ctx context.Context, gw gwclient.Client) (*gwclient.Result, error) {
		// Create result to hold all platform states
		res := gwclient.NewResult()
		var expPlatforms exptypes.Platforms
		
		// Process each platform
		for i, state := range platformStates {
			platform := platformSpecs[i]
			platformKey := platforms.Format(platform)
			log.Debugf("Processing platform %s for OCI export", platformKey)
			
			// Marshal the state with platform constraint
			def, err := state.Marshal(ctx, llb.Platform(platform))
			if err != nil {
				return nil, fmt.Errorf("failed to marshal LLB state for platform %s: %w", platformKey, err)
			}
			
			// Solve the definition to get a reference
			r, err := gw.Solve(ctx, gwclient.SolveRequest{
				Definition: def.ToPB(),
			})
			if err != nil {
				return nil, fmt.Errorf("failed to solve for platform %s: %w", platformKey, err)
			}
			
			ref, err := r.SingleRef()
			if err != nil {
				return nil, fmt.Errorf("failed to get reference for platform %s: %w", platformKey, err)
			}
			
			// Add the reference for this platform
			res.AddRef(platformKey, ref)
			log.Debugf("Added reference for platform %s", platformKey)
			
			// Add platform metadata using the correct exptypes structure
			expPlatforms.Platforms = append(expPlatforms.Platforms, exptypes.Platform{
				ID:       platformKey,
				Platform: platform,
			})
		}
		
		// Add platform metadata for multi-platform manifest (same as frontend)
		if len(platformSpecs) > 1 {
			log.Infof("Creating multi-platform manifest with %d platforms", len(platformSpecs))
		}
		
		// Always add platform metadata for consistency (same as frontend)
		dt, err := json.Marshal(expPlatforms)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal platforms: %w", err)
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, dt)
		
		return res, nil
	}, buildChannel)
	
	if err != nil {
		return fmt.Errorf("BuildKit build failed: %w", err)
	}
	
	// Extract the OCI tar file to the output directory
	tarPath := outputDir + ".tar"
	if err := extractOCITar(tarPath, outputDir); err != nil {
		return fmt.Errorf("failed to extract OCI tar: %w", err)
	}
	
	// Clean up the tar file
	os.Remove(tarPath)
	
	log.Infof("Successfully created OCI layout in %s", outputDir)
	return nil
}

// extractOCITar extracts an OCI layout tar file to a directory.
func extractOCITar(tarPath, outputDir string) error {
	// Create output directory
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	
	// Open tar file
	file, err := os.Open(tarPath)
	if err != nil {
		return fmt.Errorf("failed to open tar file: %w", err)
	}
	defer file.Close()
	
	// Create tar reader
	tr := tar.NewReader(file)
	
	// Extract all files
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break // End of tar file
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}
		
		// Build target path
		targetPath := filepath.Join(outputDir, header.Name)
		
		// Create directory for file if needed
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", targetPath, err)
		}
		
		switch header.Typeflag {
		case tar.TypeDir:
			// Create directory
			if err := os.MkdirAll(targetPath, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", targetPath, err)
			}
		case tar.TypeReg:
			// Create regular file
			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("failed to create file %s: %w", targetPath, err)
			}
			
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return fmt.Errorf("failed to copy content to %s: %w", targetPath, err)
			}
			outFile.Close()
		}
	}
	
	return nil
}

// getPlatformSuffix returns the expected image tag suffix for a platform.
func getPlatformSuffix(platform *ispec.Platform) string {
	suffix := "-" + platform.Architecture
	if platform.Variant != "" {
		suffix += "-" + platform.Variant
	}
	return suffix
}


