package utils

import (
	"fmt"
	"os"
	"regexp"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Engines map[string]string `yaml:"engines"`
}

// LoadConfig reads the config file, substitutes environment variables, and converts engine configs to strings
func LoadConfig(filename string) (Config, error) {
	var rawConfig map[string]interface{}
	var finalConfig Config

	data, err := os.ReadFile(filename)
	if err != nil {
		return finalConfig, err
	}

	// Substitute environment variables before parsing
	substitutedData := substituteEnvVars(string(data))

	// Unmarshal into a generic map
	err = yaml.Unmarshal([]byte(substitutedData), &rawConfig)
	if err != nil {
		return finalConfig, fmt.Errorf("error parsing YAML: %w", err)
	}

	// Convert each engine configuration into a string
	enginesRaw, ok := rawConfig["engines"].(map[interface{}]interface{})
	if !ok {
		return finalConfig, fmt.Errorf("invalid format for engines")
	}

	finalConfig.Engines = make(map[string]string)

	for engineName, engineConfig := range enginesRaw {
		// Marshal each engine's config back to YAML string
		engineConfigStr, err := yaml.Marshal(engineConfig)
		if err != nil {
			return finalConfig, fmt.Errorf("error marshaling engine config for %s: %w", engineName, err)
		}

		finalConfig.Engines[fmt.Sprintf("%v", engineName)] = string(engineConfigStr)
	}

	return finalConfig, nil
}

// substituteEnvVars replaces ${VAR} with the environment variable value
func substituteEnvVars(content string) string {
	re := regexp.MustCompile(`\$\{(\w+)\}`)
	return re.ReplaceAllStringFunc(content, func(match string) string {
		envVar := match[2 : len(match)-1] // Extract variable name
		value := os.Getenv(envVar)
		if value == "" {
			logrus.Fatalf("Warning: environment variable %s is not set\n", envVar)
		}
		return value
	})
}
