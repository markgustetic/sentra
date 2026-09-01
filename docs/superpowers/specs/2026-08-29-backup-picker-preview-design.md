# Backup picker — cursor-following file preview pane

**Date:** 2026-08-29 · **Status:** approved (in-session) · **Owner:** Mark

## Problem

The backup view's folder picker lists **directories only** — a backup root
is a directory by definition, so files never appear. That means the
operator commits a backup without ever seeing what is actually inside the
directory: the one question the picker exists to answer ("is this the
folder I mean?") gets no evidence beyond the folder's name.

## Decision

A **preview pane to the right of the picker**, on the configure stage
only, showing the contents of whatever the cursor points at:

- a highlighted folder row (child, `..`, or a `~` place row) → that
  folder's contents;
- the Start button → the current directory — exactly what the backup
  would include.

The pane always answers "what is inside the thing enter would act on."

### Contents

- A muted header line names the previewed directory (its base name plus
  `/`), so the pane reads as "inside X" even when the cursor is moving.
- Directories first (trailing `/`), then files with sizes right-aligned;
  both groups sorted case-insensitively, matching the picker's own sort.
- Capped at the picker's window height; a final muted `… +N more` row
  carries the remainder count.
- Empty directory → `(empty)`. Unreadable target → the reason as one
  muted line (not fatal, mirroring the picker's own unreadable-cwd rule).
- **Metadata only**: names and sizes, never file contents. `Info()` is
  lstat — symlinks are shown by name, never followed.

### Model

`dirPicker` gains a `preview` field (target path, shown entries, total
count, error) recomputed inside the existing transforms — `reload`,
`moveUp`, `moveDown`, `up`, `activate` — whenever the target changes,
and cached by path so a repaint costs nothing. This keeps the model
**pure and synchronously drivable from tests**, the same rationale the
picker already documents for its own `os.ReadDir`. `Info()` runs only
for the rows actually shown, so a huge directory costs one `ReadDir`
plus a handful of lstats per newly hovered row.

### Rendering

`BackupView.View` joins two columns with `lipgloss.JoinHorizontal`:
picker column fixed at 32 cols, a 2-col gap, preview takes the rest of
the interior. The pane renders only when it would get **at least 20
cols** (interior ≥ 54); below that it hides entirely and the view
degrades to today's layout. At the 80-col minimum the interior is ~59,
so the pane shows at ~25 cols. Every line styles plain text per-fragment
(the "never wrap an already-styled string" rule) and is bounded via
`truncateToWidth`/`spread`. The pane renders regardless of picker vs
tag-field focus — it follows the picker cursor, which stays visible.

No new keys, no focus changes, no new shell contract: the pane is pure
rendering over model state the picker already owns.

## Testing (TDD)

Model tests (drive `dirPicker` directly, hermetic temp dirs):

- preview target follows the cursor across folder, `..`, and place rows;
- Start button previews the current directory;
- cap + `+N more` count on an over-full directory;
- unreadable target shows the reason; empty directory shows `(empty)`;
- symlinked file is listed by name, not followed.

View tests (drive `BackupView`):

- no rendered line exceeds the interior width at the 80-col minimum
  (extend the `picker_width_test.go` pattern);
- the pane is absent below the width threshold;
- assertions are glyph/text-based, valid under lipgloss's Ascii profile.

## Out of scope

- Recursive size/file-count aggregation (what `filetree.go` does for
  snapshots) — a preview reads one directory level, cheaply.
- Previewing file contents, ignore-rule evaluation ("what would be
  skipped"), or any new keybinding.
