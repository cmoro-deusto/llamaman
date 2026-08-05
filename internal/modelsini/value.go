package modelsini

// ParseBoolValue interprets an INI value using llama.cpp's exact
// truthiness table (common/arg.cpp):
//
//	truthy: on, enabled, true, 1
//	falsey: off, disabled, false, 0
//
// It returns (false, false) for anything else.
func ParseBoolValue(v string) (bool, bool) {
	switch v {
	case "on", "enabled", "true", "1":
		return true, true
	case "off", "disabled", "false", "0":
		return false, true
	}
	return false, false
}
