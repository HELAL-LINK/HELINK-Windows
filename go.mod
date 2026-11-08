module nazzelha

go 1.24.0

toolchain go1.24.7

require (
	github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1
	golang.org/x/sys v0.41.0
)

replace (
	github.com/jchv/go-webview2 => ./third_party/go-webview2
	github.com/jchv/go-winloader => ./third_party/go-winloader
	golang.org/x/sys => ./third_party/x-sys
)
