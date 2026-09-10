package debloat

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	nano11go "github.com/Pandapip1/nano11-go"

	"github.com/Pandapip1/gowim/lzx"
	"github.com/Pandapip1/packer-plugin-windows-utils/datasource/patch"
)

// debloatISO is the top-level pipeline for the debloat datasource:
//
//  1. Extract the source ISO to a scratch media tree (patch.ExtractISO,
//     this module's own dependency-free ISO reader).
//  2. Call into nano11-go's own library (github.com/Pandapip1/nano11-go) to
//     debloat sources/install.wim (see runNano11GoDebloat).
//  3. Apply any caller-supplied registry_tweaks directly against that
//     debloated install.wim's registry hives, offline, via gowim's
//     regf/registry packages (see regtweaks.go) -- independent of
//     nano11-go's own DebloatOptions.SkipRegTweaks-gated registry tweak
//     pass, since it's cleanly separable library code.
//  4. Call into nano11-go again to author the final bootable ISO from the
//     (still on-disk) extracted tree, placing the tweaked install.wim at
//     sources/install.wim (see runNano11GoAuthorISO).
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

// lzxPresetOptions maps an lzx_preset HCL string to the gowim lzx package's
// preset ladder, mirroring nano11-go's own cmd/nano11-go lzxPresetFlag.Set
// (including its error message) since that mapping is not part of the
// library's public API (DebloatOptions.LZX/ISOOptions take lzx.Options
// directly, not a preset name).
func lzxPresetOptions(name string) (lzx.Options, error) {
	presets := map[string]func() lzx.Options{
		"fast":     lzx.Fast,
		"balanced": lzx.Balanced,
		"default":  lzx.DefaultOptions,
		"max":      lzx.Max,
		"none":     lzx.None,
	}
	p, ok := presets[name]
	if !ok {
		return lzx.Options{}, fmt.Errorf("invalid lzx_preset %q (want fast, balanced, default, max or none)", name)
	}
	return p(), nil
}

// debloatOptions builds nano11-go's DebloatOptions from d's HCL fields,
// translating them 1:1 into nano11-go's own DebloatOptions fields (see
// nano11Flags' doc comment: every field here defaults to false/off,
// matching nano11-go's own CLI defaults exactly). Split out from
// runNano11GoDebloat so the mapping can be unit tested without actually
// running a debloat pass.
//
// WinRE is set to WinREDonorStub unconditionally: that stage is not
// exposed as its own HCL field (see README's "Not currently exposed"
// note), but it is nano11-go CLI's own default (see cmd/nano11-go/main.go),
// and this datasource's prior subprocess-based implementation always
// invoked the CLI with no -winre-mode override, so it always got
// WinREDonorStub too. Reproducing that default here (rather than leaving
// WinRE at its zero value, WinREKeep) preserves that existing behavior.
//
// KeepNICDrivers is OR'd with KeepDrivers: nano11-go's own CLI applies that
// same implication itself (main.go: "if opts.KeepDrivers { opts.KeepNICDrivers
// = true }") before calling into the library, since DebloatOptions itself
// deliberately does not infer it (see DebloatOptions.KeepDrivers' doc
// comment). This datasource's prior subprocess-based implementation shelled
// out to that same CLI, so it inherited the implication for free; it must
// be reproduced explicitly now that the CLI is bypassed.
func debloatOptions(d *Datasource) (nano11go.DebloatOptions, error) {
	lzxOpts, err := lzxPresetOptions(d.lzxPreset)
	if err != nil {
		return nano11go.DebloatOptions{}, err
	}

	f := d.flags
	return nano11go.DebloatOptions{
		SkipAppx:            f.skipAppx,
		SkipPackages:        f.skipPackages,
		SkipFileCleanup:     f.skipFileCleanup,
		SkipWinSxSWipe:      f.skipWinSxSWipe,
		SkipRegTweaks:       f.skipRegTweaks,
		SkipServices:        f.skipServices,
		WinRE:               nano11go.WinREDonorStub,
		KeepNICDrivers:      f.keepNICDrivers || f.keepDrivers,
		KeepDrivers:         f.keepDrivers,
		RemoveWebEngines:    f.removeWebEngines,
		KeepDefenderSearch:  f.keepDefenderSearch,
		RemoveStoreApps:     f.removeStoreApps,
		RemoveUWPFrameworks: f.removeUWPFrameworks,
		RemoveAIFoundation:  f.removeAIFoundation,
		RemoveIME:           f.removeIME,
		RemoveAI:            f.removeAI,
		LZX:                 lzxOpts,
	}, nil
}

// isoOptions builds nano11-go's ISOOptions for the ISO-authoring stage,
// pointing it at the already-extracted mediaDir and the (possibly
// registry-tweaked) installWimPath produced by the debloat stage above.
// Split out for the same testability reason as debloatOptions.
func isoOptions(d *Datasource, mediaDir, installWimPath, outPath string) nano11go.ISOOptions {
	return nano11go.ISOOptions{
		Dir:              mediaDir,
		Out:              outPath,
		VolID:            d.isoVolumeID,
		InstallWim:       installWimPath,
		SkipAutounattend: d.flags.skipISOAutounattend,
		KeepExtras:       d.flags.keepISOExtras,
	}
}

// runNano11GoDebloat calls into nano11-go's install.wim debloat stage.
func runNano11GoDebloat(d *Datasource, wimPath, outPath string) error {
	opts, err := debloatOptions(d)
	if err != nil {
		return err
	}
	return nano11go.Debloat(wimPath, outPath, d.imageIndex, opts)
}

// runNano11GoAuthorISO calls into nano11-go's ISO-authoring stage.
func runNano11GoAuthorISO(d *Datasource, mediaDir, installWimPath, outPath string) error {
	return nano11go.BuildISO(isoOptions(d, mediaDir, installWimPath, outPath))
}
