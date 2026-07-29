package tui

// helpDescIndent is how far the Help view indents a description under its
// title. It is also the width budget the description table is checked against
// (see TestHelp_DescriptionsFitTheNarrowestPane), so it lives here rather than
// inline in the renderer.
const helpDescIndent = 4

// viewDescriptions is the one-line "what does this screen do" text for every
// navigable view, keyed by command ID. NewApp copies each entry into the
// registry's Command.Description, so the Help view and the rail render the same
// list in the same order and cannot drift.
//
// Every registered command MUST have an entry, and no entry may name a command
// that is not registered — TestHelp_EveryCommandDescribed states that rule so a
// view added or removed later cannot silently leave the help screen wrong.
//
// Keep each line within the width budget checked by
// TestHelp_DescriptionsFitTheNarrowestPane: a wrapped description would push the
// view past the height the shell budgeted it, and the content panel overflows
// rather than truncates.
var viewDescriptions = map[string]string{
	"dashboard":    "Repo health, last snapshot, and size timeline",
	"backup":       "Snapshot a folder into the repository",
	"snapshots":    "Browse past snapshots and inspect their files",
	"files":        "Latest snapshot's directory layout as a graph",
	"diff":         "Compare two snapshots file by file",
	"check":        "Verify repository integrity end to end",
	"doctor":       "Diagnose config, AWS access, and repo health",
	"recovery-kit": "Print a non-secret kit for disaster recovery",
	"policies":     "Manage named backup policies and run them",
	"schedule":     "Install or remove OS scheduler entries",
	"agent":        "Scan for backup risks and get recommendations",
	"restore":      "Restore a snapshot to a chosen destination",
	"prune":        "Apply retention and reclaim unused storage",
	"sync":         "Replicate this repository to a second bucket",
	"password":     "Rotate the repository passphrase",
	"settings":     "Configuration summary and app preferences",
	"setup":        "Re-run the first-run configuration wizard",
}
