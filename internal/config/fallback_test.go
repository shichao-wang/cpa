package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFallbackModelRoundTrip(t *testing.T) {
	cfg, err := Parse([]byte(`{"profiles":{"one":{"fallbackModel":["sonnet","haiku"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sonnet", "haiku"}
	if got := cfg.Profiles["one"].FallbackModel; !reflect.DeepEqual(got, want) {
		t.Fatalf("fallbackModel = %v", got)
	}
	data, err := json.Marshal(cfg.Profiles["one"])
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Profile
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roundTrip.FallbackModel, want) {
		t.Errorf("round trip = %v", roundTrip.FallbackModel)
	}
}
