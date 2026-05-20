// champion_loader.go — default ChampionGeneLoader backed by
// ChallengerRepo.GetPackageBlob + EvolvableStrategy.DecodeElite.
//
// The package blob is the source-of-truth ChallengerResultPackage JSON
// (see internal/repository/challenger.go). We only need two pieces of
// it for Tick: the strategy-private gene payload (decoded via the
// strategy itself) and the SpawnPoint (passed verbatim to Step).
package instance

import (
	"context"
	"encoding/json"
	"fmt"

	"quantlab/internal/domain"
	"quantlab/internal/repository"
	"quantlab/internal/resultpkg"
	"quantlab/internal/strategy"
)

// DefaultChampionGeneLoader pulls the package blob through
// ChallengerRepo and unwraps the bits Tick needs.
type DefaultChampionGeneLoader struct {
	Challengers *repository.ChallengerRepo
}

// Load fetches the Challenger's full package, decodes the champion
// gene through strat.DecodeElite, and returns the SpawnPoint payload
// alongside. Errors propagate verbatim; caller wraps with Tick context.
func (l *DefaultChampionGeneLoader) Load(
	ctx context.Context,
	challengerID string,
	strat strategy.EvolvableStrategy,
) (domain.Gene, resultpkg.SpawnPointPayload, error) {
	blob, err := l.Challengers.GetPackageBlob(ctx, challengerID)
	if err != nil {
		return nil, resultpkg.SpawnPointPayload{}, fmt.Errorf("champion loader: blob: %w", err)
	}

	// Parse the minimum slice we need — core.champion_gene and
	// core.spawn_point. Everything else stays opaque on disk.
	var envelope struct {
		Core struct {
			ChampionGene resultpkg.ChampionGenePayload `json:"champion_gene"`
			SpawnPoint   resultpkg.SpawnPointPayload   `json:"spawn_point"`
		} `json:"core"`
	}
	if err := json.Unmarshal(blob, &envelope); err != nil {
		return nil, resultpkg.SpawnPointPayload{}, fmt.Errorf("champion loader: unmarshal envelope: %w", err)
	}

	gene, err := strat.DecodeElite(envelope.Core.ChampionGene)
	if err != nil {
		return nil, resultpkg.SpawnPointPayload{}, fmt.Errorf("champion loader: decode elite: %w", err)
	}
	return gene, envelope.Core.SpawnPoint, nil
}
