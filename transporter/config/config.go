package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

type AppConfiguration struct {
	TransporterAddress string `json:"transporterAddress"`
	TransporterType    string `json:"transporterType"`
}

func CreateConfig(path string) *AppConfiguration {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		panic(fmt.Errorf("configuration file does not exists in this location: %s", path))
	} else if err != nil {
		panic(err)
	}
	var config AppConfiguration
	err = json.Unmarshal(data, &config)
	if err != nil {
		panic(err)
	}
	return &config
}
