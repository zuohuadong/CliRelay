package gemini

// The request normalizer owns the declaration-key migration. Keep the
// historical benchmark helpers available for the equivalence test while
// exercising the same production normalization path.
func renameToolDeclarations(input []byte) []byte {
	out, _ := ConvertGeminiRequestToGemini("", input, false)
	return out
}

func renameToolDeclarationsSlow(input []byte) []byte {
	out, _ := ConvertGeminiRequestToGemini("", input, false)
	return out
}
