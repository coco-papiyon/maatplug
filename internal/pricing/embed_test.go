package pricing

import "testing"

func TestEmbedded_ParsesShippedPricingJSON(t *testing.T) {
	doc, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Models) == 0 {
		t.Error("Embedded().Models is empty, want at least one model from the checked-in pricing.json")
	}
}
