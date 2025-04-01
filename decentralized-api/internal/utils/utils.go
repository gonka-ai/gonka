package utils

import (
	"decentralized-api/completionapi"
	"decentralized-api/utils"
	"encoding/json"
)

func GetResponseHash(bodyBytes []byte) (string, *completionapi.Response, error) {
	var response completionapi.Response
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return "", nil, err
	}

	var content string
	for _, choice := range response.Choices {
		content += choice.Message.Content
	}
	hash := utils.GenerateSHA256Hash(content)
	return hash, &response, nil
}
