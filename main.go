package main

import (
	"log"
	"net/http"

	"github.com/robertprast/goop/pkg/proxy"
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
)

func main() {
	configPath := "config.yml"
	config, err := utils.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}

	proxyHandler := proxy.NewProxyHandler(config)
	http.Handle("/", proxyHandler)
	logrus.Info("Starting proxy server on :8080")
	logrus.Fatal(http.ListenAndServe("0.0.0.0:8080", nil))
}
