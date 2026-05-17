package resultpkg

import "errors"

// Validate checks the basic invariants of SliceScore in isolation.
// The full three-state mutual exclusion is checked on CrucibleResult,
// which can see SkippedBy.
func (s *SliceScore) Validate() error {
	if s == nil {
		return errors.New("SliceScore is nil")
	}
	if s.Fatal && s.Value != nil {
		return errors.New("SliceScore: Fatal=true requires Value=nil")
	}
	return nil
}

// Validate enforces the three mutually exclusive states described on
// SliceScore plus the Window/SkippedBy enum membership.
func (r *CrucibleResult) Validate() error {
	if r == nil {
		return errors.New("CrucibleResult is nil")
	}
	if !r.Window.IsValid() {
		return errors.New("CrucibleResult: Window is not one of 6m/2y/5y/10y")
	}
	if r.BarsEvaluated < 0 {
		return errors.New("CrucibleResult: BarsEvaluated < 0")
	}
	if r.SkippedBy != nil && !r.SkippedBy.IsValid() {
		return errors.New("CrucibleResult: SkippedBy is not a valid cascade cause")
	}
	if r.Score.Fatal && r.SkippedBy != nil {
		return errors.New("CrucibleResult: Fatal and SkippedBy cannot both be set")
	}
	if !r.Score.Fatal && r.SkippedBy == nil && r.Score.Value == nil {
		return errors.New("CrucibleResult: normal window must have Score.Value != nil")
	}
	if r.SkippedBy != nil && r.Score.Value != nil {
		return errors.New("CrucibleResult: cascade-skipped window must not have Score.Value")
	}
	if r.Score.Fatal && r.Score.Value != nil {
		return errors.New("CrucibleResult: Fatal window must have Score.Value=nil")
	}
	return nil
}

// Validate checks that each window result is internally consistent.
// Note: deliberately does NOT check ScoreTotal — RawEvaluateResult does
// not carry it. ScoreTotal is filled by fitness.AggregateScoreTotal
// inside EvaluationLayer.
func (r *RawEvaluateResult) Validate() error {
	if r == nil {
		return errors.New("RawEvaluateResult is nil")
	}
	if r.Windows == nil {
		return errors.New("RawEvaluateResult: Windows is nil")
	}
	for i := range r.Windows {
		if err := r.Windows[i].Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks five-layer non-zero presence and version-triple
// consistency across Core and ReproducibilityMetadata.
func (p *ChallengerResultPackage) Validate() error {
	if p == nil {
		return errors.New("ChallengerResultPackage is nil")
	}

	if p.Core.StrategyID == "" {
		return errors.New("ChallengerResultPackage: Core.StrategyID is empty")
	}
	if p.Core.SchemaVersion == "" {
		return errors.New("ChallengerResultPackage: Core.SchemaVersion is empty")
	}
	if p.Core.FitnessVersion == "" {
		return errors.New("ChallengerResultPackage: Core.FitnessVersion is empty")
	}
	if p.Core.FingerprintVersion == "" {
		return errors.New("ChallengerResultPackage: Core.FingerprintVersion is empty")
	}

	if p.Core.SchemaVersion != p.Core.ReproducibilityMetadata.SchemaVersion {
		return errors.New("ChallengerResultPackage: Core.SchemaVersion != ReproducibilityMetadata.SchemaVersion")
	}
	if p.Core.FitnessVersion != p.Core.ReproducibilityMetadata.FitnessVersion {
		return errors.New("ChallengerResultPackage: Core.FitnessVersion != ReproducibilityMetadata.FitnessVersion")
	}
	if p.Core.FingerprintVersion != p.Core.ReproducibilityMetadata.FingerprintVersion {
		return errors.New("ChallengerResultPackage: Core.FingerprintVersion != ReproducibilityMetadata.FingerprintVersion")
	}

	if p.Core.ChampionGene.Encoding == "" {
		return errors.New("ChallengerResultPackage: ChampionGene.Encoding is empty")
	}
	if p.Core.ChampionGene.Encoding != GeneEncodingJSON {
		return errors.New("ChallengerResultPackage: ChampionGene.Encoding must be \"json\" during prototype phase")
	}

	for i := range p.Evaluation.WindowScores {
		if err := p.Evaluation.WindowScores[i].Validate(); err != nil {
			return err
		}
	}

	if !p.Promote.DecisionStatus.IsValid() {
		return errors.New("ChallengerResultPackage: Promote.DecisionStatus is not pending/promoted/rejected")
	}

	return nil
}
