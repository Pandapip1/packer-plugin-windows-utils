package debloat

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/Pandapip1/packer-plugin-windows-utils/datasource/patch"
)

// debloatISO is the top-level pipeline for the debloat datasource:
//
//  1. Extract the source ISO to a scratch media tree (patch.ExtractISO,
//     this module's own dependency-free ISO reader).
//  2. Shell out to nano11-go to debloat sources/install.wim (see
//     runNano11GoDebloat).
//  3. Apply any caller-supplied registry_tweaks directly against that
//     debloated install.wim's registry hives, offline, via gowim's
//     regf/registry packages (see regtweaks.go) -- no nano11-go/subprocess
//     involvement for this step, since it's cleanly separable library code.
//  4. Shell out to nano11-go again to author the final bootable ISO from
//     the (still on-disk) extracted tree, placing the tweaked install.wim
//     at sources/install.wim (see runNano11GoAuthorISO).
//
// It returns the path to the resulting ISO, which is left in place (in a
// temp file, per this plugin's "copy to a temp file, modify the copy,
// return its path" convention -- see patch's datasource.go) for Packer to
// use downstream.
func debloatISO(d *Datasource) (string, error) {
	mediaDir, err := os.MkdirTemp("", "debloat-media-*")
	if err != nil {
		return "", fmt.Errorf("creating media directory: %w", err)
	}
	defer os.RemoveAll(mediaDir)

	log.Printf("  Extracting ISO to %s ...", mediaDir)
	if err := patch.ExtractISO(d.isoPath, mediaDir); err != nil {
		return "", fmt.Errorf("extracting ISO: %w", err)
	}

	installWimPath := filepath.Join(mediaDir, "sources", "install.wim")
	if _, err := os.Stat(installWimPath); err != nil {
		return "", fmt.Errorf("extracted media has no sources/install.wim: %w", err)
	}

	debloatedWim, err := os.CreateTemp("", "debloat-install-*.wim")
	if err != nil {
		return "", fmt.Errorf("creating temp install.wim: %w", err)
	}
	debloatedWimPath := debloatedWim.Name()
	debloatedWim.Close()
	os.Remove(debloatedWimPath)
	defer os.Remove(debloatedWimPath)

	log.Printf("  Running nano11-go debloat pass on %s ...", installWimPath)
	if err := runNano11GoDebloat(d, installWimPath, debloatedWimPath); err != nil {
		return "", fmt.Errorf("nano11-go debloat: %w", err)
	}

	if len(d.registryTweaks) > 0 {
		log.Printf("  Applying %d registry tweak(s) ...", len(d.registryTweaks))
		if err := applyRegistryTweaksToWim(debloatedWimPath, d.registryTweaks); err != nil {
			return "", fmt.Errorf("applying registry tweaks: %w", err)
		}
	}

	dstFile, err := os.CreateTemp("", "debloated-*.iso")
	if err != nil {
		return "", fmt.Errorf("creating temp ISO: %w", err)
	}
	dstPath := dstFile.Name()
	dstFile.Close()
	os.Remove(dstPath)

	log.Printf("  Authoring debloated ISO to %s ...", dstPath)
	if err := runNano11GoAuthorISO(d, mediaDir, debloatedWimPath, dstPath); err != nil {
		os.Remove(dstPath)
		return "", fmt.Errorf("nano11-go author ISO: %w", err)
	}

	log.Printf("  Done -- debloated ISO ready.")
	return dstPath, nil
}

// debloatArgs builds the argument list for nano11-go's install.wim debloat
// stage (its -wim/-out/-image flow), translating d's HCL fields 1:1 into
// nano11-go's own flags (see nano11Flags' doc comment: every field here
// defaults to false/off, matching nano11-go's own CLI defaults exactly).
// Split out from runNano11GoDebloat so the argument mapping can be unit
// tested without actually invoking nano11-go.
func debloatArgs(d *Datasource, wimPath, outPath string) []string {
	args := []string{
		"-wim", wimPath,
		"-out", outPath,
		"-image", strconv.Itoa(d.imageIndex),
		"-lzx-preset", d.lzxPreset,
	}

	f := d.flags
	addFlag := func(set bool, name string) {
		if set {
			args = append(args, "-"+name)
		}
	}
	addFlag(f.skipAppx, "skip-appx")
	addFlag(f.skipPackages, "skip-packages")
	addFlag(f.skipFileCleanup, "skip-filecleanup")
	addFlag(f.skipWinSxSWipe, "skip-winsxs-wipe")
	addFlag(f.skipRegTweaks, "skip-regtweaks")
	addFlag(f.skipServices, "skip-services")
	addFlag(f.keepNICDrivers, "keep-nic-drivers")
	addFlag(f.keepDrivers, "keep-drivers")
	addFlag(f.removeWebEngines, "remove-web-engines")
	addFlag(f.keepDefenderSearch, "keep-defender-search")
	addFlag(f.removeAI, "remove-ai")
	addFlag(f.removeStoreApps, "remove-store-apps")
	addFlag(f.removeUWPFrameworks, "remove-uwp-frameworks")
	addFlag(f.removeAIFoundation, "remove-ai-foundation")
	addFlag(f.removeIME, "remove-ime")

	return args
}

// authorISOArgs builds the argument list for nano11-go's ISO-authoring stage
// (-iso-dir/-iso-out/-iso-install-wim), pointing it at the already-extracted
// mediaDir and the (possibly registry-tweaked) installWimPath produced by
// the debloat stage above. Split out for the same testability reason as
// debloatArgs.
func authorISOArgs(d *Datasource, mediaDir, installWimPath, outPath string) []string {
	args := []string{
		"-iso-dir", mediaDir,
		"-iso-out", outPath,
		"-iso-install-wim", installWimPath,
		"-iso-volid", d.isoVolumeID,
	}
	if d.flags.keepISOExtras {
		args = append(args, "-keep-iso-extras")
	}
	if d.flags.skipISOAutounattend {
		args = append(args, "-skip-iso-autounattend")
	}
	return args
}

// runNano11GoDebloat shells out to nano11-go's install.wim debloat stage.
func runNano11GoDebloat(d *Datasource, wimPath, outPath string) error {
	return runNano11Go(d.nano11goBinary, debloatArgs(d, wimPath, outPath))
}

// runNano11GoAuthorISO shells out to nano11-go's ISO-authoring stage.
func runNano11GoAuthorISO(d *Datasource, mediaDir, installWimPath, outPath string) error {
	return runNano11Go(d.nano11goBinary, authorISOArgs(d, mediaDir, installWimPath, outPath))
}

func runNano11Go(binary string, args []string) error {
	cmd := exec.Command(binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w\n%s", binary, args, err, out)
	}
	if len(out) > 0 {
		log.Printf("  nano11-go: %s", out)
	}
	return nil
}
