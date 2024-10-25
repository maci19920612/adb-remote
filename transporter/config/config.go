package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

type TransporterConfiguration struct {
	Address string `json:"transporterAddress"`
}

func CreateConfig(path string) *TransporterConfiguration {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		panic(fmt.Errorf("configuration file does not exists in this location: %s", path))
	} else if err != nil {
		panic(err)
	}
	var config TransporterConfiguration
	err = json.Unmarshal(data, &config)
	if err != nil {
		panic(err)
	}
	return &config
}
