package frontend

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	container_types "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

var (
	copaPath      string
	frontendImage string
	buildkitAddr  string
)

func TestMain(m *testing.M) {
	flag.StringVar(&buildkitAddr, "addr", "docker://", "buildkit address to pass through to buildctl")
	flag.StringVar(&copaPath, "copa", "./copa", "path to copa binary")
	flag.StringVar(&frontendImage, "frontend-image", "copa-frontend:test", "copa frontend image to use for testing")
	flag.Parse()

	if copaPath == "" {
		panic("missing --copa")
	}

	if frontendImage == "" {
		panic("missing --frontend-image")
	}

	// Check if buildctl is available
	if _, err := exec.LookPath("buildctl"); err != nil {
		fmt.Printf("skipping frontend tests; buildctl binary not found in path: %v\n", err)
		os.Exit(0)
	}

	// Check if docker is available
	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Printf("skipping frontend tests; docker binary not found in path: %v\n", err)
		os.Exit(0)
	}

	// Check if oras is available
	if _, err := exec.LookPath("oras"); err != nil {
		fmt.Printf("skipping frontend tests; oras binary not found in path: %v\n", err)
		os.Exit(0)
	}

	// Setup local registry for testing
	ctx := context.Background()
	setupLocalRegistry(ctx)

	// Build the frontend image
	buildFrontendImage()

	// Run tests
	ec := m.Run()

	// Cleanup
	stopLocalRegistry()

	os.Exit(ec)
}

func buildFrontendImage() {
	fmt.Printf("Building Copa frontend image: %s\n", frontendImage)
	cmd := exec.Command("docker", "build", "-f", "frontend.Dockerfile", "-t", frontendImage, ".")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic(fmt.Sprintf("failed to build frontend image: %v", err))
	}
	fmt.Printf("Frontend image built successfully: %s\n", frontendImage)
}

func setupLocalRegistry(ctx context.Context) {
	// Check if registry is already running and clean it up
	dockerCli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(fmt.Sprintf("failed to create docker client: %v", err))
	}

	containers, err := dockerCli.ContainerList(ctx, container_types.ListOptions{All: true})
	if err != nil {
		panic(fmt.Sprintf("failed to list containers: %v", err))
	}

	for i := range containers {
		container := &containers[i]
		for _, name := range container.Names {
			if name == "/registry-frontend-test" {
				// Remove existing registry
				err := dockerCli.ContainerRemove(ctx, container.ID, container_types.RemoveOptions{Force: true})
				if err != nil {
					panic(fmt.Sprintf("failed to remove existing registry container: %v", err))
				}
				break
			}
		}
	}

	// Start registry container
	fmt.Println("Starting local Docker registry for frontend tests...")
	cmd := exec.Command("docker", "run", "-d", "-p", "5000:5000", "--name", "registry-frontend-test", "registry:2")
	if err := cmd.Run(); err != nil {
		panic(fmt.Sprintf("failed to start registry container: %v", err))
	}

	// Wait for registry to be ready
	time.Sleep(3 * time.Second)
	fmt.Println("Local registry is ready at localhost:5000")
}

func stopLocalRegistry() {
	fmt.Println("Stopping local Docker registry...")
	cmd := exec.Command("docker", "rm", "-f", "registry-frontend-test")
	_ = cmd.Run() // ignore errors during cleanup
}