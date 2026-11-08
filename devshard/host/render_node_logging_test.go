package host

import (
	"context"
	"testing"

	"devshard/observability"
	"github.com/stretchr/testify/require"
)

type nodeReportingRenderer struct{ *recordedRenderer }

func (r *nodeReportingRenderer) CountTurn(ctx context.Context, model string, request []byte, turn map[string]any) (uint64, string, error) {
	observability.ObserveRenderNode(ctx, "first", "http://user:secret@node1:8000?token=secret")
	observability.ObserveRenderNode(ctx, "second", "http://node2:8000")
	return r.recordedRenderer.CountTurn(ctx, model, request, turn)
}
func (r *nodeReportingRenderer) CountPrompt(ctx context.Context, model string, request []byte) (uint64, string, error) {
	observability.ObserveRenderNode(ctx, "third", "http://node3:8000")
	return r.recordedRenderer.CountPrompt(ctx, model, request)
}

func TestChallengeRenderNodeDiagnostics(t *testing.T) {
	for _, c := range loadRecordedChallenges(t) {
		if c.sample.Name != "plain_short" {
			continue
		}
		renderer := &nodeReportingRenderer{&recordedRenderer{t: t, counts: *c.sample.Render}}
		h := &Host{renderer: renderer}
		_, count, reject, cause := h.fetchedVerdict(context.Background(), c.fetch, c.rec)
		require.Empty(t, reject, cause)
		require.Equal(t, []string{"first@http://node1:8000", "second@http://node2:8000"}, count.outputRenderNodes)
		require.Equal(t, []string{"third@http://node3:8000"}, count.inputRenderNodes)
		fields := count.logFields(c.rec.OutputTokens)
		require.Contains(t, fields, "output_render_nodes")
		require.Contains(t, fields, "input_render_nodes")
		return
	}
	t.Fatal("plain_short recording not found")
}
