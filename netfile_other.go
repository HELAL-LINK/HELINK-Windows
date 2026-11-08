//go:build !windows

package main

import (
	"net/http"
	"os"
)

// systemProxy is the environment's proxy (HTTPS_PROXY and the like).
var systemProxy = http.ProxyFromEnvironment

// Files on Linux and macOS have holes anyway.
func makeSparse(*os.File) {}
func unsparse(*os.File)   {}
