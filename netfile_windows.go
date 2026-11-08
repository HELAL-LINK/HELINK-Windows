//go:build windows

package main

import (
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ---------------------------------------------------------------------------
// The proxy set in Windows (Settings > Network > Proxy: the one the browsers use)
// ---------------------------------------------------------------------------

type winProxy struct {
	enabled bool
	servers map[string]string // "http", "https", "socks", or "" (all) -> host:port
	bypass  []string
	local   bool // <local>: names without a dot go direct
	at      time.Time
}

var (
	winProxyMu    sync.Mutex
	winProxyCache winProxy
)

func winProxySettings() winProxy {
	winProxyMu.Lock()
	defer winProxyMu.Unlock()
	if !winProxyCache.at.IsZero() && time.Since(winProxyCache.at) < 30*time.Second {
		return winProxyCache
	}
	p := winProxy{at: time.Now(), servers: map[string]string{}}
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err == nil {
		en, _, _ := k.GetIntegerValue("ProxyEnable")
		srv, _, _ := k.GetStringValue("ProxyServer")
		ovr, _, _ := k.GetStringValue("ProxyOverride")
		k.Close()
		if en != 0 && strings.TrimSpace(srv) != "" {
			p.enabled = true
			for _, part := range strings.Split(srv, ";") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				if k, v, ok := strings.Cut(part, "="); ok {
					p.servers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
				} else {
					p.servers[""] = part
				}
			}
			for _, b := range strings.Split(ovr, ";") {
				b = strings.ToLower(strings.TrimSpace(b))
				switch {
				case b == "<local>":
					p.local = true
				case b != "":
					b = strings.TrimPrefix(strings.TrimPrefix(b, "http://"), "https://")
					if i := strings.LastIndexByte(b, ':'); i > 0 && !strings.Contains(b[i:], "]") {
						b = b[:i]
					}
					p.bypass = append(p.bypass, b)
				}
			}
		}
	}
	winProxyCache = p
	return p
}

// systemProxy sends a request through the proxy set in Windows, if there is one;
// otherwise through the environment's (HTTPS_PROXY and the like).
func systemProxy(req *http.Request) (*url.URL, error) {
	p := winProxySettings()
	if !p.enabled {
		return http.ProxyFromEnvironment(req)
	}
	host := strings.ToLower(req.URL.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || (p.local && !strings.Contains(host, ".")) {
		return nil, nil
	}
	for _, b := range p.bypass {
		if ok, _ := path.Match(b, host); ok {
			return nil, nil
		}
	}
	v, scheme := p.servers[req.URL.Scheme], "http"
	if v == "" {
		v = p.servers[""]
	}
	if v == "" {
		if v = p.servers["socks"]; v == "" {
			return nil, nil
		}
		scheme = "socks5"
	}
	if !strings.Contains(v, "://") {
		v = scheme + "://" + v
	}
	u, err := url.Parse(v)
	if err != nil {
		return nil, nil
	}
	return u, nil
}

// ---------------------------------------------------------------------------
// Files with holes
// ---------------------------------------------------------------------------

const fsctlSetSparse = 0x000900c4

// makeSparse lets the file have holes: writing far into a big file then does not make
// Windows fill everything before that place with zeros first (the parts near the end
// would wait for it). Drives that cannot (FAT, exFAT) simply fill the zeros.
func makeSparse(f *os.File) {
	var n uint32
	_ = windows.DeviceIoControl(windows.Handle(f.Fd()), fsctlSetSparse, nil, 0, nil, 0, &n, nil)
}

// unsparse makes a complete file an ordinary one again.
func unsparse(f *os.File) {
	var n uint32
	in := []byte{0} // FILE_SET_SPARSE_BUFFER{SetSparse: FALSE}
	_ = windows.DeviceIoControl(windows.Handle(f.Fd()), fsctlSetSparse, &in[0], 1, nil, 0, &n, nil)
}
