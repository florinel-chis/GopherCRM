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

// JSONUnmarshal unmarshals a JSON string to a value
func JSONUnmarshal(data string, v interface{}) error {
	return json.Unmarshal([]byte(data), v)
}