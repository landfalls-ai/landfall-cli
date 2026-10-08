package tools

// TextLike reports whether read_artifact inlines a file of this content type
// (text/*, JSON, XML, YAML, CSV, SVG). `landfall artifact` answers with text
// for exactly the same set, so the console and an agent never disagree about
// which files are text.
func TextLike(contentType string) bool {
	return textLike(contentType)
}
