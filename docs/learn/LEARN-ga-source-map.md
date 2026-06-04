# LEARN — GA Source-File Map & Reading Order

A navigation aid for the Genetic Algorithm code. **Not a normative spec** —
the source of truth for design is `docs/进化计算引擎.md` (framework v5.4.1) and
`docs/策略数学引擎.md` (strategy math). Line numbers are approximate and drift;
trust the function names.

## The key split

GA is divided across the **two-layer hard boundary**:

- **Engine layer** drives the GA *loop* — population, generations, selection,
  elitism, convergence/early-stop. Strategy-agnostic.
- **Strategy layer** defines the *gene-level operators* — Sample / Clamp /
  Crossover / Mutate — because gene semantics belong to the strategy, not the
  engine.

The engine never imports strategy internals; it only calls the
`EvolvableStrategy` interface verbs.

## 1. Engine layer — the GA loop (`internal/engine/`)

- **`engine.go`** (~513 lines, core)
  - `RunEpoch` — one Epoch = `MaxGenerations` of GA against a plan. **Start here.**
  - `evaluatePopulation` — whole-generation evaluation via the worker pool.
  - `produceNextGeneration` — selection → crossover → mutation → next gen.
  - `tournamentSelect` — tournament selection.
  - `collectFatalSamples` — deterministic Fatal-audit sampling.
  - File-top `EngineConfig` comment documents the **mutation ramp + early-stop**
    knob semantics.
- **`convergence.go`** (~119) — convergence-rescue layer 1: `convergenceState` +
  `observe()`. On no-improvement it scales mutation by `MutationRampFactor`; after
  `EarlyStopPatience` no-improvement generations it early-stops.
- **`package.go`** (~215) — assembles `EpochResult` into a
  `ChallengerResultPackage` (result packaging, not GA math).

## 2. Strategy layer — gene semantics + operators (`internal/strategies/sigmoid_v1/`)

- **`sigmoid.go`** — **the GA operators live here**: `Sample` (random gene),
  `Clamp`, `Crossover`, `Mutate`, `DecodeElite`.
- **`chromosome.go`** (~228) — gene layout / semantics + `clampOne` bounds.
- Evaluation side (read when studying *how a strategy scores*, not the GA loop):
  `evaluate_window.go`, `simulator.go`, `signal.go`, `macro.go`, `step.go`,
  `market_state.go`, `release.go`, `runtime_state.go`.
- **`toy/toy.go`** — a minimal strategy; **the fastest way to see how the 14
  interface verbs are actually implemented.**

## 3. Interface contract + supporting types

- **`internal/strategy/evolvable.go`** (~155) — the `EvolvableStrategy` 14-verb
  interface + the `Adapter` interface, each verb commented. This is the *only*
  engine↔strategy contract. `contract.go` / `runtime.go` are companion types.
- **`internal/domain/types.go`** (~119) — `Gene`, `SegmentInfo`, `SpawnPoint`,
  `SliceScore`, and other gene/score primitives.
- **`internal/fitness/aggregate.go`** (~86) — `AggregateScoreTotal`: four-window
  weighting + consistency penalty — the **fitness aggregation** (called by the
  engine, not the strategy). `ghost_dca.go` — DCA dual baseline.

## Recommended reading order

1. `strategy/evolvable.go` — the 14 verbs (build the vocabulary).
2. `strategies/toy/toy.go` — minimal impl; see the verbs land.
3. `engine/engine.go`: `RunEpoch` → `produceNextGeneration` → `tournamentSelect`
   — the GA loop skeleton.
4. `engine/convergence.go` — mutation ramp / early-stop.
5. `strategies/sigmoid_v1/sigmoid.go`: `Crossover` / `Mutate` — the real operators.
6. `fitness/aggregate.go` — how fitness is aggregated.

## Cross-references

- Priority GA tests (executable spec): see `CLAUDE.md` §"Priority Tests" —
  `TestEvaluateDeterministic`, `TestCrossoverBlockFidelity`,
  `TestMutationScaleLinearity`, `TestCascadeShortCircuit`, etc.
- GA performance analysis (bottleneck = per-bar full-window indicator recompute):
  tracked in the project memory `ga-cpu-optimization`.
