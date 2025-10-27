package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dagger/harbor-cli/internal/dagger"
	"dagger/harbor-cli/utils"
)

func (m *HarborCli) PublishImageAndSign(
	ctx context.Context,
	source *dagger.Directory,
	registry string,
	registryUsername string,
	registryPassword *dagger.Secret,
	imageTags []string,
	// +optional
	githubToken *dagger.Secret,
	// +optional
	actionsIdTokenRequestToken *dagger.Secret,
	// +optional
	actionsIdTokenRequestUrl string,
) (string, error) {
	m.init(ctx, source)

	imageAddrs, err := m.PublishImage(ctx, registry, registryUsername, imageTags, registryPassword)
	if err != nil {
		return "", fmt.Errorf("failed to publish image: %w", err)
	}

	_, err = m.Sign(
		ctx,
		githubToken,
		actionsIdTokenRequestUrl,
		actionsIdTokenRequestToken,
		registryUsername,
		registryPassword,
		imageAddrs[0],
	)
	if err != nil {
		return "", fmt.Errorf("failed to sign image: %w", err)
	}

	fmt.Printf("Signed image: %s\n", imageAddrs)
	return imageAddrs[0], nil
}

func (m *HarborCli) PublishImage(
	ctx context.Context,
	registry, registryUsername string,
	// +optional
	// +default=["latest"]
	imageTags []string,
	registryPassword *dagger.Secret,
) ([]string, error) {
	version := m.AppVersion
	archs := []string{"amd64", "arm64"}

	// Building Binaries
	dist := dag.Directory()
	dist, err := m.build(ctx, dist)
	if err != nil {
		return []string{}, err
	}

	releaseImages := []*dagger.Container{}

	for i, tag := range imageTags {
		imageTags[i] = strings.TrimSpace(tag)
		if strings.HasPrefix(imageTags[i], "v") {
			imageTags[i] = strings.TrimPrefix(imageTags[i], "v")
		}
	}
	fmt.Printf("provided tags: %s\n", imageTags)

	// Get current time for image creation timestamp
	creationTime := time.Now().UTC().Format(time.RFC3339)

	for _, arch := range archs {
		// Defining binary file name
		binName := fmt.Sprintf("harbor-cli_%s_%s_%s", m.AppVersion, "linux", arch)

		ctr := dag.Container(dagger.ContainerOpts{Platform: dagger.Platform("linux/" + arch)}).
			From("alpine:latest").
			WithWorkdir("/").
			WithFile("/harbor", dist.File(binName)).
			WithExec([]string{"ls", "-al"}).
			WithExec([]string{"./harbor", "version"}).
			// Add required metadata labels for ArtifactHub
			WithLabel("org.opencontainers.image.created", creationTime).
			WithLabel("org.opencontainers.image.description", "Harbor CLI - A command-line interface for CNCF Harbor, the cloud native registry!").
			WithLabel("io.artifacthub.package.readme-url", "https://raw.githubusercontent.com/goharbor/harbor-cli/main/README.md").
			WithLabel("org.opencontainers.image.source", "https://github.com/goharbor/harbor-cli").
			WithLabel("org.opencontainers.image.version", version).
			WithLabel("io.artifacthub.package.license", "Apache-2.0").
			WithEntrypoint([]string{"/harbor"})
		releaseImages = append(releaseImages, ctr)
	}

	imageAddrs := []string{}
	for _, imageTag := range imageTags {
		addr, err := dag.Container().WithRegistryAuth(registry, registryUsername, registryPassword).
			Publish(ctx,
				fmt.Sprintf("%s/%s/harbor-cli:%s", registry, "harbor-cli", imageTag),
				dagger.ContainerPublishOpts{PlatformVariants: releaseImages},
			)
		if err != nil {
			panic(err)
		}
		fmt.Printf("Published image address: %s\n", addr)
		imageAddrs = append(imageAddrs, addr)
	}

	return imageAddrs, nil
}

func (s *HarborCli) build(ctx context.Context, dist *dagger.Directory) (*dagger.Directory, error) {
	goarch := []string{"amd64", "arm64"}

	for _, arch := range goarch {
		// Defining binary file name
		binName := fmt.Sprintf("harbor-cli_%s_%s_%s", s.AppVersion, "linux", arch)

		builder := dag.Container().
			From("golang:"+s.GoVersion).
			WithMountedCache("/go/pkg/mod", dag.CacheVolume("go-mod-"+s.GoVersion)).
			WithEnvVariable("GOMODCACHE", "/go/pkg/mod").
			WithMountedCache("/go/build-cache", dag.CacheVolume("go-build-"+s.GoVersion)).
			WithEnvVariable("GOCACHE", "/go/build-cache").
			WithMountedDirectory("/src", s.Source).
			WithWorkdir("/src").
			WithEnvVariable("GOOS", "linux").
			WithEnvVariable("GOARCH", arch).
			WithEnvVariable("CGO_ENABLED", "0")

		gitCommit, _ := builder.WithExec([]string{"git", "rev-parse", "--short", "HEAD", "--always"}).Stdout(ctx)
		buildTime := time.Now().UTC().Format(time.RFC3339)

		ldflagsArgs := utils.LDFlags(ctx, s.AppVersion, s.GoVersion, buildTime, gitCommit)

		builder = builder.WithExec([]string{
			"bash", "-c",
			fmt.Sprintf(`set -ex && go env && go build -v -ldflags "%s" -o /bin/%s /src/cmd/harbor/main.go`, ldflagsArgs, binName),
		})

		file := builder.File("/bin/" + binName)                            // Taking file from container
		dist = dist.WithFile(fmt.Sprintf("%s/%s", "linux", binName), file) // Adding file(bin) to dist directory
	}

	return dist, nil
}

// Sign signs a container image using Cosign, works also with GitHub Actions
func (m *HarborCli) Sign(ctx context.Context,
	// +optional
	githubToken *dagger.Secret,
	// +optional
	actionsIdTokenRequestUrl string,
	// +optional
	actionsIdTokenRequestToken *dagger.Secret,
	registryUsername string,
	registryPassword *dagger.Secret,
	imageAddr string,
) (string, error) {
	registryPasswordPlain, _ := registryPassword.Plaintext(ctx)

	cosing_ctr := dag.Container().From("cgr.dev/chainguard/cosign")

	// If githubToken is provided, use it to sign the image
	if githubToken != nil {
		if actionsIdTokenRequestUrl == "" || actionsIdTokenRequestToken == nil {
			return "", fmt.Errorf("actionsIdTokenRequestUrl (exist=%s) and actionsIdTokenRequestToken (exist=%t) must be provided when githubToken is provided", actionsIdTokenRequestUrl, actionsIdTokenRequestToken != nil)
		}
		fmt.Printf("Setting the ENV Vars GITHUB_TOKEN, ACTIONS_ID_TOKEN_REQUEST_URL, ACTIONS_ID_TOKEN_REQUEST_TOKEN to sign with GitHub Token")
		cosing_ctr = cosing_ctr.WithSecretVariable("GITHUB_TOKEN", githubToken).
			WithEnvVariable("ACTIONS_ID_TOKEN_REQUEST_URL", actionsIdTokenRequestUrl).
			WithSecretVariable("ACTIONS_ID_TOKEN_REQUEST_TOKEN", actionsIdTokenRequestToken)
	}

	return cosing_ctr.WithSecretVariable("REGISTRY_PASSWORD", registryPassword).
		WithExec([]string{"cosign", "env"}).
		WithExec([]string{
			"cosign", "sign", "--yes", "--recursive",
			"--registry-username", registryUsername,
			"--registry-password", registryPasswordPlain,
			imageAddr,
			"--timeout", "1m",
		}).Stdout(ctx)
}
