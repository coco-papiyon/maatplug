package pricing

import (
	_ "embed"
	"encoding/json"
)

// embeddedJSON is the pricing.json this package ships with, baked into the
// maatplug/maatplug-web binaries at build time so cost calculations work
// with no separate data file to deploy. cmd/maatplug-pricing-fetch is what
// keeps this file current as of each release (build.bat runs it before
// packaging); this package never re-fetches it at runtime.
//
//go:embed pricing.json
var embeddedJSON []byte

// Embedded parses the pricing.json this package was built with.
func Embedded() (Document, error) {
	var doc Document
	if err := json.Unmarshal(embeddedJSON, &doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}
