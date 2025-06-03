package report

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	trivyTypes "github.com/aquasecurity/trivy/pkg/types"
	"github.com/project-copacetic/copacetic/pkg/types/unversioned"
	log "github.com/sirupsen/logrus"
)

type TrivyParser struct{}

func parseTrivyReport(file string) (*trivyTypes.Report, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var msr trivyTypes.Report
	if err = json.Unmarshal(data, &msr); err != nil {
		return nil, &ErrorUnsupported{err}
	}
	return &msr, nil
}

func NewTrivyParser() *TrivyParser {
	return &TrivyParser{}
}

func (t *TrivyParser) Parse(file string) (*unversioned.UpdateManifest, error) {
	report, err := parseTrivyReport(file)
	if err != nil {
		return nil, err
	}

	updates := unversioned.UpdateManifest{
		Metadata: unversioned.Metadata{
			OS: unversioned.OS{
				Type:    string(report.Metadata.OS.Family),
				Version: report.Metadata.OS.Name,
			},
			Config: unversioned.Config{
				Arch:    report.Metadata.ImageConfig.Architecture,
				Variant: report.Metadata.ImageConfig.Variant,
			},
		},
	}

	// Process all results for both OS and language packages
	osFound := false
	for i := range report.Results {
		r := &report.Results[i]
		switch r.Class {
		case trivyTypes.ClassOSPkg:
			if osFound {
				return nil, errors.New("unexpected multiple results for os-pkgs")
			}
			osFound = true
			// Handle OS packages
			for i := range r.Vulnerabilities {
				vuln := &r.Vulnerabilities[i]
				if vuln.FixedVersion != "" {
					updates.Updates = append(updates.Updates, unversioned.UpdatePackage{
						Name:             vuln.PkgName,
						InstalledVersion: vuln.InstalledVersion,
						FixedVersion:     vuln.FixedVersion,
						VulnerabilityID:  vuln.VulnerabilityID,
					})
				}
			}

		case trivyTypes.ClassLangPkg:
			// Handle language-specific packages (Node.js for now)
			if r.Type == "npm" || r.Type == "nodejs" || r.Type == "yarn" || r.Type == "pnpm" || r.Type == "node-pkg" {
				log.Infof("Found Node.js vulnerabilities in %s", r.Target)
				for i := range r.Vulnerabilities {
					vuln := &r.Vulnerabilities[i]
					if vuln.FixedVersion != "" {
						// For Node.js packages, Trivy may return multiple fixed versions
						// We'll take the first one that's appropriate for the major version
						fixedVersion := extractAppropriateFixedVersion(vuln.InstalledVersion, vuln.FixedVersion)
						if fixedVersion != "" {
							updates.NodeUpdates = append(updates.NodeUpdates, unversioned.UpdatePackage{
								Name:             vuln.PkgName,
								InstalledVersion: vuln.InstalledVersion,
								FixedVersion:     fixedVersion,
								VulnerabilityID:  vuln.VulnerabilityID,
							})
						}
					}
				}
			}
		}
	}

	// Validate we have at least some updates
	if len(updates.Updates) == 0 && len(updates.NodeUpdates) == 0 {
		return nil, errors.New("no patchable vulnerabilities found in report")
	}

	if len(updates.NodeUpdates) > 0 {
		log.Infof("Found %d OS package updates and %d Node.js package updates",
			len(updates.Updates), len(updates.NodeUpdates))
	}

	return &updates, nil
}

// extractAppropriateFixedVersion selects the most appropriate fixed version from a comma-separated list
// For Node.js packages, we prefer to stay within the same major version if possible
func extractAppropriateFixedVersion(installedVersion, fixedVersions string) string {
	// If there's only one version, return it
	if !strings.Contains(fixedVersions, ",") {
		return strings.TrimSpace(fixedVersions)
	}
	
	// Split the versions
	versions := strings.Split(fixedVersions, ",")
	
	// Extract major version from installed version
	installedParts := strings.Split(installedVersion, ".")
	if len(installedParts) == 0 {
		// If we can't parse, just return the first fixed version
		return strings.TrimSpace(versions[0])
	}
	installedMajor := installedParts[0]
	
	// Look for a fixed version with the same major version
	for _, v := range versions {
		v = strings.TrimSpace(v)
		fixedParts := strings.Split(v, ".")
		if len(fixedParts) > 0 && fixedParts[0] == installedMajor {
			return v
		}
	}
	
	// If no same-major version found, return the first one
	// This might require manual intervention but it's better than nothing
	return strings.TrimSpace(versions[0])
}
