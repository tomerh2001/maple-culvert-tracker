package helpers

import (
	"os"
	"testing"
)

// TestRealSampleDebug instruments the parse of the user's real screenshot
// (1079x1021, smooth 2x, 4-bit-quantized colors). Skipped when absent.
func TestRealSampleDebug(t *testing.T) {
	data, err := os.ReadFile("../../../provided/real-sample.png")
	if err != nil {
		t.Skip("no real sample")
	}
	font, err := LoadGPQFont()
	if err != nil {
		t.Fatal(err)
	}
	members := []string{"HTomer", "StarryLeyva", "Senpapi", "Nunez", "Xenpapi",
		"unrenquited", "StellaMaris", "SunCharon", "GenSharpe", "Ecatepec",
		"Nighteee", "RadeXenon", "Heartorias", "xSieunp", "IDKJagg",
		"toobooae", "Hruriel"}

	entries, err := ParseParticipationImage(data, members, font)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("parsed %d entries", len(entries))
	for _, e := range entries {
		t.Logf("  %s: %d", e.Name, e.Score)
	}
}
