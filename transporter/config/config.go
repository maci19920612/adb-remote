package config

import (
	"encoding/json"
	"os"
)

type TransporterConfiguration struct {
	Address string `json:"transporterAddress"`
}

func CreateConfig(path string) (*TransporterConfiguration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config TransporterConfiguration
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}
