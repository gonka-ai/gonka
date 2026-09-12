# [DRAFT]: Simulation Checklist

Entry: `simulation.py` -> `experiment.json` -> `visualize.py` -> `dynamic-coeff*.png`. Environments and parameters are in `config.py`.

Scenarios to simulate:

1. Baseline convergence. Targets are set to 0.45/0.10/0.45 and 1/3 each. Expect all shares to enter target zones within ~30 epochs and converge (speed up further). The base model (fixed) may stay off target since `f` does not adjust it directly.
2. Step size variants. Compare a fixed step, a step that only shrinks (halves on direction changes), and the adaptive step (halves on direction changes, doubles otherwise). A fixed step causes endless oscillation across the target zone boundaries; a shrink-only step becomes too slow to adjust after a direction change.
3. Target variations. Test a Kimi-heavy share of 0.60, an extreme 0.80, a target of 0, and targets summing to 0.5. The base model absorbs the remaining allocation.
4. Redeployment threshold epsilon. Determine the minimum threshold where best-response switches terminate. At `epsilon = 0.001`, nodes switch back and forth endlessly. At `epsilon >= 0.005`, switching stops within a few passes, yielding the same final shares. The threshold depends on the coefficient impact of a single node's move, requiring verification for each hardware distribution.
5. Convergence speed. Reach within 10% of final values by epoch 5. Error-proportional step sizes do not help because the share error remains small while coefficients traverse dead zones. Bisection over `[coeff_min, coeff_max]` reaches the target on responsive hosts (see 7 for why it fails otherwise). To achieve fast convergence without fragile controllers, `coeff_min` should be placed close to the expected clearing parity.
6. Determinism. Ensure identical final values for the same hardware distribution. Fix GPU counts per class and randomize only host assignment. Final effective coefficients must be identical across runs without path dependence.
7. Laggy hosts. A fraction of hosts update allocations only every 3-5 epochs. The ~5% climb must remain robust. Faster controllers (bisection, large steps, raise-then-decay) can settle on suboptimal allocations because they act on stale shares.
8. Network growth. Simulate 10 new hosts joining at epoch 50. Shares must re-enter the target zones within ~1 epoch.