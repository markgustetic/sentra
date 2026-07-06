package setup

// Engine sequences the side-effecting steps of setup — AWS auth + bucket
// prep, config write, repo init — over an injected Effects seam. It contains
// NO huh forms, NO stdout writes, and NO cobra: the cli driver adds progress
// printing/huh repair prompts around it, and the TUI wizard drives it from
// tea messages. This is the shared behavior contract both front ends reuse.
type Engine struct {
	eff Effects
}

// NewEngine returns an Engine backed by eff.
func NewEngine(eff Effects) *Engine { return &Engine{eff: eff} }
