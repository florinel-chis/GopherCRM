package utils

import "encoding/json"

// JSONMarshal marshals a value to JSON string, returns empty string on error
func JSONMarshal(v interface{}) string {
	bytes, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(bytes)
}
