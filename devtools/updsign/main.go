// updsign writes the signed latest.json that HELINK reads to update itself (update.go): the
// version, what is new in it, and its files (the installer, the portable zip) with their sizes,
// SHA-256 and addresses, signed with HELINK's Ed25519 key.
//
//	go run ./devtools/updsign -key ~/helink-release-keys/update-ed25519.key -version 2.3.1 \
//	    -setup release/HELINK-2.3.1-Setup.exe -portable release/HELINK-2.3.1-portable.zip \
//	    -notes docs/release-notes/2.3.1 -ref <commit> -out release/latest.json
//
// The files are published at the root of github.com/HELAL-LINK/HELINK-Windows
// (devtools/publish_update.sh): their addresses name the commit that holds them (-ref), so they
// never change, on GitHub and on its copy at jsDelivr.
package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type file struct {
	URLs   []string `json:"urls"`
	Size   int64    `json:"size"`
	SHA256 string   `json:"sha256"`
}

type manifest struct {
	App      string              `json:"app"`
	Version  string              `json:"version"`
	Date     string              `json:"date"`
	Notes    map[string][]string `json:"notes"`
	Setup    *file               `json:"setup"`
	Portable *file               `json:"portable"`
}

func main() {
	keyPath := flag.String("key", "", "the private key (base64 Ed25519 seed)")
	version := flag.String("version", "", "the version, like 2.3.1")
	setup := flag.String("setup", "", "the installer")
	portable := flag.String("portable", "", "the portable zip")
	notes := flag.String("notes", "", "what is new: <prefix>.ar.txt and <prefix>.en.txt, one line each")
	repo := flag.String("repo", "HELAL-LINK/HELINK-Windows", "the GitHub repository")
	ref := flag.String("ref", "", "the commit (or tag) that holds the files (default v<version>)")
	release := flag.Bool("release", false, "the files are assets of the GitHub release v<version> (its Releases page), not files of the repository")
	out := flag.String("out", "latest.json", "where latest.json goes")
	pubOnly := flag.Bool("pubkey", false, "only print the public key of -key (it must be the one in update.go)")
	flag.Parse()
	if *keyPath == "" {
		flag.Usage()
		os.Exit(2)
	}
	seedText, err := os.ReadFile(*keyPath)
	if err != nil {
		log.Fatal(err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(seedText)))
	if err != nil || len(seed) != ed25519.SeedSize {
		log.Fatal("bad key file")
	}
	key := ed25519.NewKeyFromSeed(seed)
	if *pubOnly {
		fmt.Println(base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)))
		return
	}
	if *version == "" || *setup == "" || *portable == "" {
		flag.Usage()
		os.Exit(2)
	}
	tag := *ref
	if tag == "" {
		tag = "v" + *version
	}
	if *release {
		releaseTag = "v" + *version
	}
	m := manifest{App: "helink-windows", Version: *version, Date: time.Now().UTC().Format("2006-01-02"),
		Notes: map[string][]string{"ar": lines(*notes + ".ar.txt"), "en": lines(*notes + ".en.txt")}}
	m.Setup = describe(*setup, *repo, tag)
	m.Portable = describe(*portable, *repo, tag)
	body, err := json.Marshal(m)
	if err != nil {
		log.Fatal(err)
	}
	sig := ed25519.Sign(key, body)
	signed, _ := json.MarshalIndent(map[string]string{
		"manifest": base64.StdEncoding.EncodeToString(body),
		"sig":      base64.StdEncoding.EncodeToString(sig),
	}, "", "  ")
	if err := os.WriteFile(*out, append(signed, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	pub := key.Public().(ed25519.PublicKey)
	fmt.Printf("%s: %s, signed with the key %s\n%s\n", *out, *version, base64.StdEncoding.EncodeToString(pub), body)
}

// releaseTag, when set (-release), is the GitHub release whose assets the files are.
var releaseTag string

// describe gives a file's size, SHA-256, and its two addresses at the commit (or tag) that holds it.
func describe(p, repo, tag string) *file {
	f, err := os.Open(p)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		log.Fatal(err)
	}
	name := filepath.Base(p)
	if releaseTag != "" {
		return &file{Size: n, SHA256: hex.EncodeToString(h.Sum(nil)), URLs: []string{
			"https://github.com/" + repo + "/releases/download/" + releaseTag + "/" + name,
		}}
	}
	return &file{Size: n, SHA256: hex.EncodeToString(h.Sum(nil)), URLs: []string{
		"https://raw.githubusercontent.com/" + repo + "/" + tag + "/" + name,
		"https://cdn.jsdelivr.net/gh/" + repo + "@" + tag + "/" + name,
	}}
}

func lines(p string) []string {
	f, err := os.Open(p)
	if err != nil {
		log.Printf("no notes in %s", p)
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}
