package main

import (
	"github.com/robertprast/goop/pkg/proxy"
	"github.com/robertprast/goop/pkg/proxy/openai_schema"
	"log"
	"net/http"

	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
)

func main() {
	configPath := "config.yml"
	config, err := utils.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}

	openAIProxyHandler := openai_proxy.NewHandler(config)

	proxyHandler := proxy.NewProxyHandler(config)
	http.Handle("/", proxyHandler)
	http.Handle("/openai-proxy/", openAIProxyHandler)

	logrus.Info("Starting engine_proxy server on :8080")
	logrus.Fatal(http.ListenAndServe("0.0.0.0:8080", nil))
}
