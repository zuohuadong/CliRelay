package chat_completions

import "strconv"

const geminiCLIFunctionThoughtSignature = "skip_thought_signature_validator"

func itoa(value int) string {
	return strconv.Itoa(value)
}
