package main

import (
	"net/http"

	"github.com/robertprast/goop/pkg/proxy"
	"github.com/sirupsen/logrus"
)

func main() {
	proxyHandler := proxy.NewProxyHandler()
	http.Handle("/", proxyHandler)
	logrus.Info("Starting proxy server on :8080")
	logrus.Fatal(http.ListenAndServe(":8080", nil))
}
