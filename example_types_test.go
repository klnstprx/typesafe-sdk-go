package typesafe_test

import (
	"encoding/json"
	"testing"
)

// The custom type in ExampleSystemOneAs must fail closed and accept zero.
func TestBillingResponseFailsClosed(t *testing.T) {
	valid := map[string]string{
		"zero": `{"answers":{"billing":{"type":"noul","noul":0}}}`,
		"one":  `{"answers":{"billing":{"type":"noul","noul":1}}}`,
	}
	for name, body := range valid {
		var r billingResponse
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatal(err)
		}
		if err := r.Validate(); err != nil {
			t.Errorf("%s: Validate() = %v", name, err)
		}
	}
	invalid := map[string]string{
		"answers absent": `{}`,
		"answer missing": `{"answers":{}}`,
		"answer null":    `{"answers":{"billing":null}}`,
		"wrong type":     `{"answers":{"billing":{"type":"choice","noul":1}}}`,
		"type missing":   `{"answers":{"billing":{"noul":1}}}`,
		"noul missing":   `{"answers":{"billing":{"type":"noul"}}}`,
		"noul null":      `{"answers":{"billing":{"type":"noul","noul":null}}}`,
	}
	for name, body := range invalid {
		var r billingResponse
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatal(err)
		}
		if err := r.Validate(); err == nil {
			t.Errorf("%s: Validate() accepted %s", name, body)
		}
	}
}
