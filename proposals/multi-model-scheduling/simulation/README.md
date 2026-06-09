# Multi-Model Scheduling — Simulation

Agent-based simulation backing the parameter choices in [the GIP](../README.md).
Includes an **interactive HTML simulation** (`index.html`) that can be opened
in a browser, plus Python scripts for the heavier analyses that produced the
GIP's calibration data.

## Interactive simulation

A single-page interactive visualization is at `index.html`. It runs entirely
in the browser (no Python or server needed; just open the file).

### Running

The page is self-contained — open `index.html` directly in any modern browser
(double-click in Finder, drag into a browser tab, etc.). The sweep-data JSON
is embedded in the HTML so no server or local files are needed.

To share with a reviewer, send the file directly; no setup or configuration
required on their end.

If you regenerate `sweep_data.json` via `python3 defaults_sweep.py`, you'll
also need to re-embed it into `index.html` for the changes to show in the
"Why these defaults?" tab:

```sh
python3 -c "
import json, re
data = json.load(open('sweep_data.json'))
html = open('index.html').read()
import re as r
html = r.sub(r'const EMBEDDED_SWEEP_DATA = [^;]+;', 'const EMBEDDED_SWEEP_DATA = ' + json.dumps(data, separators=(',', ':')) + ';', html, count=1)
open('index.html', 'w').write(html)
"
```

### What you see

- **Live simulation tab.** 256 operators across 3 hardware tiers serve 5 models
  with fluctuating demand. Watch the agent's decisions play out — colored dots
  show each operator's current model, line charts track supply/demand/earnings
  over time, metrics summarize aggregate behaviour. Use the controls panel to
  adjust the reputation function, agent behavior, market conditions, and
  display speed.
- **Why these defaults? tab.** For each GIP-tunable parameter, a sweep curve
  showing how performance varies with that parameter while all others are held
  at GIP defaults. The teal dashed line marks the default. The simulation's
  result: the system is **robust by design** — wide bands around each default
  give essentially identical performance, and only extreme values clearly
  hurt. The defaults are chosen as round numbers in that robust zone.
- **About tab.** What the simulation models and what it abstracts away.

### Setup for browsers that block `fetch()` from `file://`

If you must open `index.html` directly with `file://`, the "Why these
defaults?" tab will show "Sweep data not yet loaded." Either use a local
HTTP server as above, or paste the contents of `sweep_data.json` inline
into the HTML's `loadSweepData()` function.

## Python analysis scripts

### Setup

```sh
python3 -m venv venv
source venv/bin/activate
pip install numpy matplotlib
```

### Files

- `sim.py` — core simulation: operators, models, iterated cheap-talk per
  epoch, reputation-weighted predicted supply, EV-based switching decision.
  Knobs at the top of the file.
- `sweep_fixed_L.py` — sweeps `(threshold, smoothness)` × population lying
  rate `L`. Produces PNG plots showing how market efficiency varies. Also
  runs a Nash-deviation analysis at the best `(threshold, smoothness)`.
- `nash_full.py` — comprehensive Nash analysis at one `(threshold,
  smoothness)` config across four realism regimes (clean, reduced
  iterations, partial information, combined). Identifies the equilibrium
  personal lying rate operators converge to.
- `defaults_sweep.py` — sweeps every GIP-default parameter under adversarial
  conditions (small population, partial information, aggressive demand
  volatility) so that bad parameter choices become visible. Writes
  `sweep_data.json` consumed by the HTML simulation's "Why these defaults?"
  tab.

### Running

Each script writes its outputs (text + PNGs) to the current directory.
Run from this directory after activating the venv:

```sh
python3 sweep_fixed_L.py   # ~4 min — Nash analysis + heatmaps
python3 nash_full.py       # ~20 min — comprehensive Nash sweep
python3 defaults_sweep.py  # ~1 min — feeds HTML sim's defaults tab
```

## Interpreting results

The key metrics:

- **Mean earnings per operator** — what an operator actually cares about.
  More sensitive than market efficiency and the primary metric in the
  defaults-tab visualizations.
- **Market efficiency** — fraction of demand-weighted value pot the
  network captures. 1.0 = every model with positive demand has at least
  one operator on it. With enough operators per model this saturates near
  1.0 even under bad parameter choices.
- **Avg lying rate L** — population-mean of operators' personal bluffing
  probability.
- **Truth rate** — verified fulfillment rate of past announcements,
  averaged across the population. Should track `1 - avg_L` if agents are
  consistent.
- **Nash deviation gain** — how much EV a single operator gains by
  switching to a different L while the rest of the population stays put.
  Positive = population L is not Nash-stable.

## Headline results

At GIP defaults (`threshold = 0.70, smoothness = 0.15, lookback = 20,
switch_threshold = 0.05, switch_cooldown = 5`):

- **System is robust to parameter choice.** Within wide ranges of every
  individual parameter, earnings vary by <2% across configurations.
  Operators can tune within reason and won't hurt themselves.
- **Extreme values break things.** `switch_cooldown = 30` (~1 month) drops
  earnings ~3%. Other parameters degrade gracefully at extremes.
- **Population-optimal lying rate is L = 0** (perfect honesty maximizes
  collective welfare).
- **Nash equilibrium L under realistic friction is ~10-20%** —
  operators rationally bluff occasionally to disrupt herding.
- **Gap between Nash and population-optimal is small (~5% efficiency
  loss).** The system tolerates the expected strategic behavior gracefully.
- **Systematic liars self-correct.** Even in a population of liars, an
  individual operator can gain 5-30% EV by being more honest. The
  reputation mechanism pushes the population back toward moderate honesty.

## Calibration

If you want to argue for different defaults:

1. Edit the sweep ranges in `defaults_sweep.py`.
2. Re-run the sweep.
3. Compare earnings curves against the GIP's defaults.
4. Open `index.html` to see how your modified parameters perform in the
   live simulation.
5. Submit a PR with the simulation outputs.

Reviewers should be able to reproduce your results from the same seed
(`rng_seed=42` is the default).
