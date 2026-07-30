package setup

// Engine sequences the side-effecting steps of setup — AWS auth + bucket
// prep, config write, repo init — over an injected Effects seam. It contains
// NO huh forms, NO stdout writes, and NO cobra: the TUI wizard is the only
// sequencer, driving it from tea messages. `sentra setup` is a thin CLI
// launcher for that same wizard, not a second driver of the engine.
type Engine struct {
	eff Effects
}

// NewEngine returns an Engine backed by eff.
func NewEngine(eff Effects) *Engine { return &Engine{eff: eff} }
