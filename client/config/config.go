package config

import (
	"encoding/json"
	"os"
)

type ClientConfiguration struct {
	TransporterAddress string `json:"transporterAddress"`
}

func CreateConfig() (*ClientConfiguration, error) {
	data, err := os.ReadFile("./config.json")
	if err != nil {
		return nil, err
	}
	config := ClientConfiguration{}
	err = json.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}
