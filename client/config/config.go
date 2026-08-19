package config

import (
	"encoding/json"
	"os"
)

const DefaultConfigPath = "./config.json"

type ClientConfiguration struct {
	TransporterAddress string `json:"transporterAddress"`
}

func CreateConfig() (*ClientConfiguration, error) {
	return LoadConfig(DefaultConfigPath)
}

func LoadConfig(path string) (*ClientConfiguration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	config := ClientConfiguration{}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}
