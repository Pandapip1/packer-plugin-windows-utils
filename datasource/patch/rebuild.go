package patch

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// patchISORebuild extracts srcPath's contents to a working directory,
// optionally patches the no-prompt EFI boot images in place there, injects
// extraDrivers into a "$WinPeDriver$" folder, and repacks the result into a
// new bootable ISO. It returns the path to the new ISO.
func patchISORebuild(srcPath string, patchNoBoot bool, extraDrivers []string) (string, error) {
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
// extracted media.
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

	if runtime.GOOS == "windows" {
		return repackWithOscdimg(mediaDir, etfsbootRel, efisysRel, dstPath)
	}
	return repackWithXorriso(mediaDir, etfsbootRel, efisysRel, dstPath)
}

// repackWithXorriso rebuilds the ISO using xorriso, per
// https://forum.proxmox.com/threads/how-to-inject-vioscsi-driver-into-windows-install-win-boot-win-using-linux-tools-only.161239/
func repackWithXorriso(mediaDir, etfsbootRel, efisysRel, dstPath string) error {
	cmd := exec.Command("xorriso",
		"-as", "mkisofs",
		"-iso-level", "3",
		"-full-iso9660-filenames",
		"-joliet", "-joliet-long",
		"-eltorito-boot", filepath.ToSlash(etfsbootRel),
		"-no-emul-boot", "-boot-load-size", "8", "-boot-info-table",
		"-eltorito-catalog", "boot/boot.cat",
		"-eltorito-alt-boot",
		"-e", filepath.ToSlash(efisysRel), "-no-emul-boot",
		"-o", dstPath,
		mediaDir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("xorriso failed: %w\n%s", err, out)
	}
	return nil
}

// repackWithOscdimg rebuilds the ISO using oscdimg (Windows ADK), per
// https://learn.microsoft.com/en-us/windows-hardware/manufacture/desktop/oscdimg-command-line-options
func repackWithOscdimg(mediaDir, etfsbootRel, efisysRel, dstPath string) error {
	bootdata := fmt.Sprintf("2#p0,e,b%s#pEF,e,b%s", filepath.FromSlash(etfsbootRel), filepath.FromSlash(efisysRel))
	cmd := exec.Command("oscdimg",
		"-bootdata:"+bootdata,
		"-u1", "-udfver102",
		mediaDir, dstPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("oscdimg failed: %w\n%s", err, out)
	}
	return nil
}
