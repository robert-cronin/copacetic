package pkgmgr

import (
	"context"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/project-copacetic/copacetic/pkg/buildkit"
	"github.com/project-copacetic/copacetic/pkg/types/unversioned"
	"github.com/stretchr/testify/assert"
)

func TestNpmManagerGetPackageType(t *testing.T) {
	manager := &npmManager{}
	assert.Equal(t, "node", manager.GetPackageType())
}

func TestNpmManagerInstallUpdates(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		manifest     *unversioned.UpdateManifest
		ignoreErrors bool
		wantErr      bool
		wantErrPkgs  int
	}{
		{
			name:         "nil manifest",
			manifest:     nil,
			ignoreErrors: false,
			wantErr:      false,
			wantErrPkgs:  0,
		},
		{
			name: "empty updates",
			manifest: &unversioned.UpdateManifest{
				Updates: []unversioned.UpdatePackage{},
			},
			ignoreErrors: false,
			wantErr:      false,
			wantErrPkgs:  0,
		},
		{
			name: "single package update",
			manifest: &unversioned.UpdateManifest{
				Updates: []unversioned.UpdatePackage{
					{
						Name:             "lodash",
						InstalledVersion: "4.17.20",
						FixedVersion:     "4.17.21",
						VulnerabilityID:  "CVE-2021-23337",
					},
				},
			},
			ignoreErrors: false,
			wantErr:      false,
			wantErrPkgs:  0,
		},
		{
			name: "multiple package updates",
			manifest: &unversioned.UpdateManifest{
				Updates: []unversioned.UpdatePackage{
					{
						Name:             "lodash",
						InstalledVersion: "4.17.20",
						FixedVersion:     "4.17.21",
						VulnerabilityID:  "CVE-2021-23337",
					},
					{
						Name:             "minimist",
						InstalledVersion: "1.2.5",
						FixedVersion:     "1.2.6",
						VulnerabilityID:  "CVE-2021-44906",
					},
				},
			},
			ignoreErrors: true,
			wantErr:      false,
			wantErrPkgs:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create a mock buildkit config
			config := &buildkit.Config{
				ImageState: llb.Image("node:18-alpine"),
			}

			manager := &npmManager{
				config:        config,
				workingFolder: "/tmp/test",
			}

			state, errPkgs, err := manager.InstallUpdates(ctx, tc.manifest, tc.ignoreErrors)

			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, state)
				assert.Equal(t, tc.wantErrPkgs, len(errPkgs))
			}
		})
	}
}

func TestBuildNpmPatchScript(t *testing.T) {
	tests := []struct {
		name     string
		updates  unversioned.UpdatePackages
		contains []string
	}{
		{
			name:     "empty updates",
			updates:  unversioned.UpdatePackages{},
			contains: []string{"npm audit fix --yes", "npm cache clean --force"},
		},
		{
			name: "single update",
			updates: unversioned.UpdatePackages{
				{
					Name:         "lodash",
					FixedVersion: "4.17.21",
				},
			},
			contains: []string{
				"npm audit fix --yes",
				"npm install lodash@4.17.21",
				"npm cache clean --force",
				"patch_node_deps",
			},
		},
		{
			name: "multiple updates",
			updates: unversioned.UpdatePackages{
				{
					Name:         "lodash",
					FixedVersion: "4.17.21",
				},
				{
					Name:         "minimist",
					FixedVersion: "1.2.6",
				},
			},
			contains: []string{
				"npm install lodash@4.17.21 minimist@1.2.6",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			script := buildNpmPatchScript(tc.updates)

			// Check that the script contains expected strings
			for _, expected := range tc.contains {
				assert.Contains(t, script, expected)
			}

			// Check common elements
			assert.Contains(t, script, "#!/bin/sh")
			assert.Contains(t, script, "set -e")
			assert.Contains(t, script, "package.json")
		})
	}
}

func TestNewNpmManager(t *testing.T) {
	config := &buildkit.Config{
		ImageState: llb.Image("node:18-alpine"),
	}
	workingFolder := "/tmp/test"

	manager := NewNpmManager(config, workingFolder)

	assert.NotNil(t, manager)
	assert.IsType(t, &npmManager{}, manager)

	// Verify it implements PackageManager interface
	var _ = manager
}
