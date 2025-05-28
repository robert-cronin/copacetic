package manual

import (
	"context"
	"testing"

	"github.com/Azure/dalec/test/testenv"
	"github.com/distribution/reference"
	"github.com/moby/buildkit/client/llb"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/project-copacetic/copacetic/pkg/patch"
	"github.com/stretchr/testify/require"
)

func TestValidManualRule_LLB(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short")
	}

	// ---------- build-time constants ----------
	const (
		baseRef = "docker.io/library/nginx:1.21.6"
		tagOut  = "patched"
	)

	// ---------- spin up a throw-away BuildKit builder ----------
	ctx := context.Background()
	env := testenv.New() // docker-in-docker buildx builder :contentReference[oaicite:0]{index=0}

	// RunTest gives us a gateway client wired to the builder’s daemon.
	env.RunTest(ctx, t, func(ctx context.Context, c gwclient.Client) {
		// 1. Parse refs
		base, err := reference.ParseNamed(baseRef)
		require.NoError(t, err)

		patched, err := reference.WithTag(base, tagOut)
		require.NoError(t, err)

		ch := make(chan struct{})
		defer close(ch)

		patch.WithGatewayClient()

		// 2. Call your patching logic.
		//
		// This is where you refactor copa so it has a function that accepts the
		// gateway client rather than shelling out to `copa patch -a ...`.
		//
		//     err = copa.Patch(ctx, c, copa.PatchOpts{      // <— pseudo-API
		//         Image:      base.String(),
		//         ManualRule: reportFile,
		//         Tag:        tagOut,
		//     })
		//     require.NoError(t, err)
		//
		// For now, you can still exec the binary *inside* the buildx builder by
		// setting BUILDKIT_HOST to the env’s daemon address if you want a quick
		// bridge while refactoring.

		// 3. Build an LLB state for the patched image
		patchedState := llb.Image(patched.String()) // remote-image source node :contentReference[oaicite:1]{index=1}

		// 4. Marshal + Solve through the gateway to get a Reference we can poke at
		def, err := patchedState.Marshal(ctx)
		require.NoError(t, err)

		res, err := c.Solve(ctx, gwclient.SolveRequest{
			Definition: def.ToPB(),
		})
		require.NoError(t, err)

		ref, err := res.SingleRef()
		require.NoError(t, err)

		// 5. Assert the binary we replaced is really there
		_, err = ref.ReadFile(ctx, gwclient.ReadRequest{
			Filename: "/bin/foo",
		})
		require.NoError(t, err, "/bin/foo should exist in patched image")
	})
}
