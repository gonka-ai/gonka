# Dynamic coefficient experiment

This executable imports the production `x/inference/coefficients` package. It
does not duplicate coefficient adjustment or dilution logic.

From `inference-chain`:

```bash
bash x/inference/coefficients/sim/run.sh
```

Artifacts are written to `x/inference/coefficients/sim/output/`:

- `experiment.json` contains per-epoch shares, base coefficients, effective
  coefficients, allocation passes, and GPU rewards.
- `experiment.svg` plots those series.

Edit `config.json`, copy it, or pass another config:

```bash
bash x/inference/coefficients/sim/run.sh \
  -config x/inference/coefficients/sim/my-config.json \
  -output /tmp/coeff-experiment
```
