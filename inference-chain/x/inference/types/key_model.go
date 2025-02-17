package types

const ModelKeyPrefix = "Model/value/"

func ModelKey(
	id string,
) []byte {
	return stringKey(id)
}
