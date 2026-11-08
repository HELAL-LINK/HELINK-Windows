// mkres builds rsrc_windows_amd64.syso: the app icon, the manifest and the version details.
package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

func load(p string) image.Image {
	f, err := os.Open(p)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		panic(err)
	}
	return img
}

func main() {
	dir := os.Args[1]
	out := os.Args[2]
	ver := os.Args[3]
	var imgs []image.Image
	for _, s := range []int{256, 64, 48, 40, 32, 24, 20, 16} {
		imgs = append(imgs, load(filepath.Join(dir, fmt.Sprintf("icon-%d.png", s))))
	}
	icon, err := winres.NewIconFromImages(imgs)
	if err != nil {
		panic(err)
	}
	rs := winres.ResourceSet{}
	if err := rs.SetIcon(winres.ID(1), icon); err != nil {
		panic(err)
	}
	var v [4]uint16
	fmt.Sscanf(ver, "%d.%d.%d", &v[0], &v[1], &v[2])
	rs.SetManifest(winres.AppManifest{
		Identity:            winres.AssemblyIdentity{Name: "HELINK.App", Version: v},
		Description:         "HELINK",
		Compatibility:       winres.Win10AndAbove,
		ExecutionLevel:      winres.AsInvoker,
		DPIAwareness:        winres.DPIPerMonitorV2,
		LongPathAware:       true,
		UseCommonControlsV6: true,
	})
	vi := version.Info{}
	vi.SetFileVersion(ver)
	vi.SetProductVersion(ver)
	for k, v := range map[string]string{
		version.ProductName:      "HELINK",
		version.FileDescription:  "HELINK",
		version.CompanyName:      "Helal Technology",
		version.LegalCopyright:   "© 2026 Ali Hossam. HELINK is a trademark of Ali Hossam.",
		version.OriginalFilename: "HELINK.exe",
		version.InternalName:     "HELINK",
	} {
		if err := vi.Set(0x0409, k, v); err != nil {
			panic(err)
		}
	}
	rs.SetVersionInfo(vi)
	f, err := os.Create(out)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := rs.WriteObject(f, winres.ArchAMD64); err != nil {
		panic(err)
	}
	fmt.Println("wrote", out)
}
