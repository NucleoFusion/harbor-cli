package main

import (
	"context"
	"fmt"

	"dagger/harbor-cli/internal/dagger"
)

// Generates Software Bill of Materials for the archive files
func (m *HarborCli) SBOM(ctx context.Context,
	buildDir *dagger.Directory,
	// +ignore=[".gitignore"]
	// +defaultPath="."
	source *dagger.Directory,
) (*dagger.Directory, error) {
	if !m.IsInitialized {
		err := m.init(ctx, source)
		if err != nil {
			return nil, err
		}
	}

	entries, err := buildDir.Entries(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not read dist directory: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("dist directory is empty — run build first")
	}

	goos := []string{"linux", "darwin", "windows"}
	goarch := []string{"amd64", "arm64"}

	sbomFiles := dag.Directory()

	for _, os := range goos {
		for _, arch := range goarch {
			archiveName := fmt.Sprintf("harbor-cli_%s_%s_%s", m.AppVersion, os, arch)
			if os == "windows" {
				archiveName += ".zip"
			} else {
				archiveName += ".tar.gz"
			}

			cmd := []string{
				"syft", fmt.Sprintf("/input/%s", archiveName),
				"-o", "cyclonedx-json",
				">", fmt.Sprintf("/out/%s.sbom.json", archiveName),
			}

			sbom := dag.Container().
				From("anchore/syft:latest").
				WithMountedDirectory("/input", buildDir.Directory("archive")).
				WithMountedDirectory("/out", sbomFiles).
				WithExec(cmd)

			sbomFiles = sbomFiles.WithFile(fmt.Sprintf("%s.sbom.json", archiveName), sbom.File(fmt.Sprintf("/out/%s.sbom.json", "archiveName")))
		}
	}

	buildDir = buildDir.WithDirectory("sbom", sbomFiles)

	return buildDir, nil
}
