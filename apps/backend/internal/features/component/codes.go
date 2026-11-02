package component

// Codes returns the unique validator rule codes in catalog order
// (no-stage-wrapper appears in two phases but is listed once).
func Codes() []string {
	seen := make(map[string]bool, len(ruleCatalog))
	out := make([]string, 0, len(ruleCatalog))
	for _, r := range ruleCatalog {
		if !seen[r.Code] {
			seen[r.Code] = true
			out = append(out, r.Code)
		}
	}
	return out
}
