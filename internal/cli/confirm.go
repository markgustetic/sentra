package cli

import "github.com/charmbracelet/huh"

// huhConfirm runs a single-field huh confirm form with the supplied
// labels and returns the user's answer. Three of the cli commands
// (`prune`, `backup apply`, `agent`) wire essentially identical
// production confirm callbacks; consolidating the body here keeps
// the per-command wrappers single-line and the prompt-UX defaults
// (return false on form error, render Affirmative/Negative labels
// verbatim) in one place.
//
// The wrappers below differ only in their Affirmative / Negative
// strings — wording is per-command because the safe default depends
// on the operation:
//
//   - prune  → "Yes, delete"   / "No, abort"  (delete is destructive)
//   - apply  → "Yes, snapshot" / "No, abort"  (snapshot is creative)
//   - agent  → "Yes, apply"    / "No, skip"   (skip moves to the next rec)
//
// huhConfirm is unexported because every external caller goes
// through a public Huh*Confirm wrapper that bakes in the right pair.
func huhConfirm(prompt, affirmative, negative string) (bool, error) {
	var confirmed bool
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(prompt).
				Affirmative(affirmative).
				Negative(negative).
				Value(&confirmed),
		),
	)
	if err := form.Run(); err != nil {
		return false, err
	}
	return confirmed, nil
}

// HuhConfirm is the production Confirm implementation for `sentra
// prune`. Wired up by main.go; tests inject their own callback to
// keep the run deterministic.
//
// Affirmative is "Yes, delete" and the negative is "No, abort" —
// wording designed so the choice reads unambiguously when the cursor
// lands on the default ("No, abort" is the safer pick).
func HuhConfirm(prompt string) (bool, error) {
	return huhConfirm(prompt, "Yes, delete", "No, abort")
}

// HuhBackupApplyConfirm is the production Confirm implementation for
// `sentra backup apply`. Affirmative reads as the action ("Yes,
// snapshot") so the operator confirms what they're authorizing rather
// than a generic "Yes."
func HuhBackupApplyConfirm(prompt string) (bool, error) {
	return huhConfirm(prompt, "Yes, snapshot", "No, abort")
}

// HuhAgentConfirm is the production Confirm callback for the agent's
// per-recommendation prompt flow. Negative is "No, skip" — declining
// one recommendation moves to the next, it does not abort the run.
func HuhAgentConfirm(prompt string) (bool, error) {
	return huhConfirm(prompt, "Yes, apply", "No, skip")
}
