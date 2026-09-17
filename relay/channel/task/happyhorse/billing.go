package happyhorse

import "strconv"

// resolutionRatio returns the relative multiplier used by New API's task
// billing path. The configured model ratio supplies the two-times-per-second
// baseline; these values only differentiate HappyHorse resolution tiers.
func resolutionRatio(modelName, resolution string) float64 {
	if isEditModel(modelName) {
		if resolution == "1080P" {
			return 16.0 / 9
		}
		return 1
	}
	switch resolution {
	case "480P":
		return 1
	case "1080P":
		return 8.0 / 3
	default:
		return 2
	}
}

func usageFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
