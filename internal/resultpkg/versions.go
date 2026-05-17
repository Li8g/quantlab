// Package resultpkg holds frozen boundary types for evolution task results,
// shared between the engine, persistence layer, and HTTP API.
//
// Versions live in versions.go (this file), enums in enums.go, structs in
// types.go, and Validate methods in validate.go. No other package may declare
// these version constants — all readers (HTTP responses, result-package
// assembly, startup checks, Docker env validation) must import them from here.
//
// Grep guard:
//
//	grep -rn '"v5.3.3"\|"v1-raw-std"\|"fp-v1"' internal/ | grep -v internal/resultpkg/versions.go
//
// should return no results outside this file (test fixtures excepted).
package resultpkg

const (
	// SchemaVersionV533 corresponds to the frozen JSON schema baseline
	// (docs/进化计算引擎_数据契约.md, internal version v5.3.3). It tracks
	// the result-package shape; bumping requires migrating persisted blobs.
	SchemaVersionV533 = "v5.3.3"

	// FitnessVersionV1RawStd is the v1 fitness formula with λ_cons = 0.3
	// using raw standard deviation as the consistency penalty.
	// Challengers with different fitness_version must NOT be compared by score.
	FitnessVersionV1RawStd = "v1-raw-std"

	// FitnessVersionV2ZScore is reserved for a future z-score variant.
	// Not in use during the prototype phase; declared so call sites that
	// might emit it (e.g. forward-compat audit) reference the constant
	// instead of a string literal.
	FitnessVersionV2ZScore = "v2-zscore"

	// FingerprintVersionV1 is the v1 gene fingerprint quantization scheme
	// (fp-v1). Changes to quantization logic require a version bump.
	FingerprintVersionV1 = "fp-v1"

	// CurrentSchemaVersion is the version active in this build. Startup
	// MUST fail-fast if this disagrees with the baseline doc or with any
	// other component (e.g. Docker image label) that advertises a schema.
	CurrentSchemaVersion = SchemaVersionV533
)
