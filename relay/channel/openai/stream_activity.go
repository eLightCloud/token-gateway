package openai

import "github.com/tidwall/gjson"

func isRecognizedChatStreamData(data string) bool {
	if !gjson.Valid(data) {
		return false
	}
	for _, path := range []string{"id", "object", "model", "choices", "usage", "error"} {
		if gjson.Get(data, path).Exists() {
			return true
		}
	}
	return false
}
