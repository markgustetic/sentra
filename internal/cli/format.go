package cli

// emptyDash renders an empty string as "-" for compact command summaries and
// tables.
func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
