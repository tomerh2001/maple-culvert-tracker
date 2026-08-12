package helpers

import (
	"strconv"

	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/valkey-io/valkey-go"
)

func GetSandbagThresholdMultiplier(vk *valkey.Client, tenantID string) float64 {
	sandbagThreshold := .7
	v := apiredis.OPTIONAL_CONF_SANDBAG_THRESHOLD.For(tenantID).GetWithDefault(vk, "")
	if v != "" {
		v2, err := strconv.ParseFloat(v, 10)
		if err == nil {
			sandbagThreshold = v2
		}
	}

	return sandbagThreshold
}

func GetSandbagThresholdScore(vk *valkey.Client, tenantID string, lastKnownGoodScore int64) int64 {
	threshold := int64(float64(lastKnownGoodScore) * GetSandbagThresholdMultiplier(vk, tenantID))
	// if int64(lastKnownGoodScore)-threshold > data.MaxCulvertScoreThreshold {
	// 	threshold = lastKnownGoodScore - data.MaxCulvertScoreThreshold
	// }
	// removing this temporarily to test output, characters nowadays are giga strong

	return threshold
}
