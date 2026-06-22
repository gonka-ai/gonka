package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"devshard/e2e/testutil"
)

// Test flow:
//  1. Send three OpenAI-style chat completion requests through devshardctl.
//  2. Assert that every completion response includes choices.
//  3. Drive extra completion rounds until one host reaches at least two
//     completed validations.
//  4. Finalize the devshard session through devshardctl.
//  5. Verify the stable settlement contract:
//     escrow id, version, state root, nonce, signatures, and no duplicate
//     signature slot ids, with completed validations included.
func TestE2E_HappyPath(t *testing.T) {
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	images := requiredImages(t)
	env := startHappyPathEnv(ctx, t, images)

	client := &http.Client{Timeout: testutil.DefaultRequestTimeout}
	testutil.SendCompletions(t, client, env.clientURL, "hello", 3)

	testutil.DriveUntilValidationObserved(t, client, env.clientURL)

	settlement := testutil.FinalizeSession(t, client, env.clientURL)
	testutil.RequireSettlementContract(t, settlement)
	testutil.RequireValidationTargetContract(t, settlement, 2)
}
