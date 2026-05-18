//go:build !dev && !debug && !development

package main

import "net/http"

func registerCtlDebugRoutes(*http.ServeMux, *Proxy) {}
