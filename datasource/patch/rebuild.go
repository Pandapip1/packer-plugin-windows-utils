package patch

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Pandapip1/gowim/iso"
)

// patchISORebuild extracts srcPath's contents to a working directory,
// optionally patches the no-prompt EFI boot images in place there, injects
// extraDrivers into a "$WinPeDriver$" folder, optionally applies
// registryTweaks against sources/install.wim's imageIndex'th image (offline,
// via applyRegistryTweaksToWim -- see regtweaks.go), and repacks the result
// into a new bootable ISO. It returns the path to the new ISO.
func patchISORebuild(srcPath string, patchNoBoot bool, extraDrivers []string, registryTweaks []RegistryTweak, imageIndex int, enableSSH bool) (string, error) {
	mediaDir, err := os.MkdirTemp("", "patch-media-*")
	if err != nil {
		return "", fmt.Errorf("creating media directory: %w", err)
	}
	defer os.RemoveAll(mediaDir)

	log.Printf("  Extracting ISO to %s ...", mediaDir)
	if err := isoExtractAll(srcPath, mediaDir); err != nil {
		return "", fmt.Errorf("extracting ISO: %w", err)
	}

	if patchNoBoot {
		if err := patchNoBootPromptDir(srcPath, mediaDir); err != nil {
			return "", err
		}
	}

	log.Printf("  Injecting %d driver(s) into $WinPeDriver$ ...", len(extraDrivers))
	if err := injectDrivers(mediaDir, extraDrivers); err != nil {
		return "", err
	}

	if enableSSH {
		installWimPath, err := findByBasename(mediaDir, "install.wim")
		if err != nil {
			return "", fmt.Errorf("locating install.wim for enable_ssh: %w", err)
		}
		log.Printf("  Installing OpenSSH.Server offline into install.wim image %d ...", imageIndex)
		if err := installOpenSSHToWim(installWimPath, imageIndex); err != nil {
			return "", fmt.Errorf("enabling ssh: %w", err)
		}
	}

	if len(registryTweaks) > 0 {
		installWimPath, err := findByBasename(mediaDir, "install.wim")
		if err != nil {
			return "", fmt.Errorf("locating install.wim for registry_tweaks: %w", err)
		}
		log.Printf("  Applying %d registry tweak(s) to install.wim image %d ...", len(registryTweaks), imageIndex)
		if err := applyRegistryTweaksToWim(installWimPath, imageIndex, registryTweaks); err != nil {
			return "", fmt.Errorf("applying registry tweaks: %w", err)
		}
	}

	dstFile, err := os.CreateTemp("", "patched-*.iso")
	if err != nil {
		return "", fmt.Errorf("creating temp ISO: %w", err)
	}
	dstPath := dstFile.Name()
	dstFile.Close()
	os.Remove(dstPath)

	log.Printf("  Repacking ISO to %s ...", dstPath)
	if err := repackISO(mediaDir, dstPath); err != nil {
		os.Remove(dstPath)
		return "", err
	}

	log.Printf("  Done — patched ISO ready.")
	return dstPath, nil
}

// injectDrivers copies each driver's containing directory into a
// "$WinPeDriver$" folder at the root of the extracted media. WinPE
// automatically discovers and loads drivers placed there during boot, and
// Windows Setup does the same during installation — no boot.wim/install.wim
// modification is required.
func injectDrivers(mediaDir string, driverInfPaths []string) error {
	base := filepath.Join(mediaDir, "$WinPeDriver$")
	for i, infPath := range driverInfPaths {
		srcDir := filepath.Dir(infPath)
		dstDir := filepath.Join(base, fmt.Sprintf("%d_%s", i, filepath.Base(srcDir)))
		if err := copyDirContents(srcDir, dstDir); err != nil {
			return fmt.Errorf("copying driver %s: %w", infPath, err)
		}
		log.Printf("  Added driver %s", infPath)
	}
	return nil
}

func copyDirContents(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFileContents(path, target)
	})
}

// findByBasename returns the path of the first file under root whose name
// matches name case-insensitively.
func findByBasename(root, name string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return err
		}
		if !d.IsDir() && strings.EqualFold(d.Name(), name) {
			found = path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("%s not found in extracted media", name)
	}
	return found, nil
}

// repackISO builds a new bootable ISO from mediaDir, preserving the BIOS
// (etfsboot.com) and UEFI (efisys.bin) El Torito boot entries found in the
// extracted media. It uses gowim/iso, a pure-Go ISO 9660/UDF/El Torito/Joliet
// writer, so no external ISO authoring tool (xorriso, oscdimg) is invoked.
//
// UDF (with large files recorded in UDF only) is used rather than plain
// ISO 9660 multi-extent, matching what real Windows installation media
// actually is: confirmed via `7z l` against a real Windows 11 ISO reporting
// `Type = Udf`. See iso.Options.LargeFilesUDFOnly's doc comment for why the
// ECMA-119 multi-extent alternative is left unused here.
//
// HybridMBR is also enabled: it costs nothing extra (no external assets
// needed, since the UEFI El Torito entry is already being written) and makes
// the resulting ISO also dd-able to a USB stick for BIOS+UEFI hybrid boot, a
// zero-cost superset of the old xorriso/oscdimg invocations' behavior.
func repackISO(mediaDir, dstPath string) error {
	etfsboot, err := findByBasename(mediaDir, "etfsboot.com")
	if err != nil {
		return fmt.Errorf("locating BIOS boot file: %w", err)
	}
	efisys, err := findByBasename(mediaDir, "efisys.bin")
	if err != nil {
		return fmt.Errorf("locating UEFI boot file: %w", err)
	}
	etfsbootRel, err := filepath.Rel(mediaDir, etfsboot)
	if err != nil {
		return err
	}
	efisysRel, err := filepath.Rel(mediaDir, efisys)
	if err != nil {
		return err
	}

	b := iso.New(&iso.Options{
		Level:             iso.Level3,
		Joliet:            true,
		UDF:               true,
		LargeFilesUDFOnly: true,
		BootCatalogPath:   "boot/boot.cat",
		HybridMBR:         true,
		BootEntries: []iso.BootEntry{
			{
				ImagePath:     filepath.ToSlash(etfsbootRel),
				Platform:      iso.BootPlatformX86,
				LoadSectors:   8,
				BootInfoTable: true,
			},
			{
				ImagePath: filepath.ToSlash(efisysRel),
				Platform:  iso.BootPlatformUEFI,
			},
		},
	})
	if err := b.AddTree("", mediaDir); err != nil {
		return fmt.Errorf("adding media tree: %w", err)
	}

	out, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := b.WriteTo(out); err != nil {
		return fmt.Errorf("writing ISO: %w", err)
	}
	return nil
}
