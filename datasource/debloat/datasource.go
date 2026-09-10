// Package debloat implements the "windows-utils-debloat" Packer datasource.
//
// It debloats a Windows install image the same way the nano11-go CLI
// (github.com/Pandapip1/nano11-go) does -- AppX/servicing-package/WinSxS/
// file-cleanup/registry-tweak/service removal against install.wim, entirely
// offline via the gowim libraries (no DISM, no mounted image, no admin
// rights) -- and then optionally bakes in caller-supplied registry tweaks
// (registry_tweaks) before authoring the final bootable ISO.
//
// nano11-go's entire removal/authoring pipeline lives in its own `package
// main` (no non-main packages were split out for library reuse), so rather
// than fork/refactor that upstream repo this datasource shells out to a
// built nano11-go binary for the debloat and ISO-authoring stages (see
// nano11go.go). The one piece of the pipeline that genuinely is
// cleanly-separable library code is the registry hive read/modify/write
// step, which lives in gowim's regf/registry/wim packages -- those ARE
// consumed directly as Go module dependencies (see go.mod's replace
// directives) so registry_tweaks can be applied with no subprocess and no
// nano11-go-specific plumbing (see regtweaks.go).
package debloat

import (
	"fmt"
	"os/exec"
	"strconv"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/zclconf/go-cty/cty"
)

// registryTweakType is the cty object type of one entry in registry_tweaks.
var registryTweakType = cty.Object(map[string]cty.Type{
	"hive":  cty.String,
	"path":  cty.String,
	"name":  cty.String,
	"type":  cty.String,
	"value": cty.String,
})

// RegistryTweak is one caller-requested offline registry edit, applied
// against the debloated install.wim's registry hives after nano11-go's own
// debloat pass and before the ISO is authored. This is exactly how you'd
// pre-bake e.g. LocalAccountTokenFilterPolicy=1 (SOFTWARE\Microsoft\Windows\
// CurrentVersion\Policies\System) into the image itself, instead of setting
// it at runtime via an Autounattend.xml specialize-pass RunSynchronousCommand
// / reg.exe invocation.
type RegistryTweak struct {
	// Hive is one of "SYSTEM", "SOFTWARE", "DEFAULT", "SAM", "COMPONENTS", or
	// "NTUSER.DAT" (Users\Default\NTUSER.DAT), matching gowim/registry's
	// Hive* constants.
	Hive string
	// Path is the key path within Hive, backslash-separated and without a
	// leading backslash, e.g. `Microsoft\Windows\CurrentVersion\Policies\System`.
	Path string
	// Name is the value name within Path.
	Name string
	// Type is "dword" or "string" (case-insensitive).
	Type string
	// Value is the value data: a base-10 (or "0x"-prefixed base-16) integer
	// literal for Type "dword", or a literal string for Type "string".
	Value string
}

// nano11Flags mirrors the subset of nano11-go's own stage flags this
// datasource exposes as HCL fields (see nano11go.go's debloatArgs, which
// turns these into the actual -flag arguments). Field names/defaults match
// nano11-go's main.go 1:1 so leaving every field in this struct at its zero
// value reproduces nano11-go's own CLI defaults exactly.
type nano11Flags struct {
	skipAppx            bool
	skipPackages        bool
	skipFileCleanup     bool
	skipWinSxSWipe      bool
	skipRegTweaks       bool
	skipServices        bool
	keepNICDrivers      bool
	keepDrivers         bool
	removeWebEngines    bool
	keepDefenderSearch  bool
	removeAI            bool
	removeStoreApps     bool
	removeUWPFrameworks bool
	removeAIFoundation  bool
	removeIME           bool
	keepISOExtras       bool
	skipISOAutounattend bool
}

type Datasource struct {
	isoPath        string
	nano11goBinary string
	imageIndex     int
	lzxPreset      string
	isoVolumeID    string
	flags          nano11Flags
	registryTweaks []RegistryTweak
}

func (d *Datasource) ConfigSpec() hcldec.ObjectSpec {
	return hcldec.ObjectSpec{
		"iso_path":        &hcldec.AttrSpec{Name: "iso_path", Type: cty.String, Required: true},
		"nano11go_binary": &hcldec.AttrSpec{Name: "nano11go_binary", Type: cty.String, Required: false},
		"image_index":     &hcldec.AttrSpec{Name: "image_index", Type: cty.Number, Required: false},
		"lzx_preset":      &hcldec.AttrSpec{Name: "lzx_preset", Type: cty.String, Required: false},
		"iso_volume_id":   &hcldec.AttrSpec{Name: "iso_volume_id", Type: cty.String, Required: false},

		"skip_appx":             &hcldec.AttrSpec{Name: "skip_appx", Type: cty.Bool, Required: false},
		"skip_packages":         &hcldec.AttrSpec{Name: "skip_packages", Type: cty.Bool, Required: false},
		"skip_file_cleanup":     &hcldec.AttrSpec{Name: "skip_file_cleanup", Type: cty.Bool, Required: false},
		"skip_winsxs_wipe":      &hcldec.AttrSpec{Name: "skip_winsxs_wipe", Type: cty.Bool, Required: false},
		"skip_reg_tweaks":       &hcldec.AttrSpec{Name: "skip_reg_tweaks", Type: cty.Bool, Required: false},
		"skip_services":         &hcldec.AttrSpec{Name: "skip_services", Type: cty.Bool, Required: false},
		"keep_nic_drivers":      &hcldec.AttrSpec{Name: "keep_nic_drivers", Type: cty.Bool, Required: false},
		"keep_drivers":          &hcldec.AttrSpec{Name: "keep_drivers", Type: cty.Bool, Required: false},
		"remove_web_engines":    &hcldec.AttrSpec{Name: "remove_web_engines", Type: cty.Bool, Required: false},
		"keep_defender_search":  &hcldec.AttrSpec{Name: "keep_defender_search", Type: cty.Bool, Required: false},
		"remove_ai":             &hcldec.AttrSpec{Name: "remove_ai", Type: cty.Bool, Required: false},
		"remove_store_apps":     &hcldec.AttrSpec{Name: "remove_store_apps", Type: cty.Bool, Required: false},
		"remove_uwp_frameworks": &hcldec.AttrSpec{Name: "remove_uwp_frameworks", Type: cty.Bool, Required: false},
		"remove_ai_foundation":  &hcldec.AttrSpec{Name: "remove_ai_foundation", Type: cty.Bool, Required: false},
		"remove_ime":            &hcldec.AttrSpec{Name: "remove_ime", Type: cty.Bool, Required: false},
		"keep_iso_extras":       &hcldec.AttrSpec{Name: "keep_iso_extras", Type: cty.Bool, Required: false},
		"skip_iso_autounattend": &hcldec.AttrSpec{Name: "skip_iso_autounattend", Type: cty.Bool, Required: false},

		"registry_tweaks": &hcldec.AttrSpec{Name: "registry_tweaks", Type: cty.List(registryTweakType), Required: false},
	}
}

func (d *Datasource) OutputSpec() hcldec.ObjectSpec {
	return hcldec.ObjectSpec{
		"debloated_iso_path": &hcldec.AttrSpec{Name: "debloated_iso_path", Type: cty.String},
	}
}

func (d *Datasource) Configure(configs ...interface{}) error {
	// Defaults chosen to match nano11-go's own CLI defaults exactly (see
	// nano11Flags' doc comment): all stage-skip/opt-in flags false, image 1
	// (the common single-edition case), the "fast" LZX preset, and
	// nano11-go's own default ISO volume label.
	d.nano11goBinary = "nano11-go"
	d.imageIndex = 1
	d.lzxPreset = "fast"
	d.isoVolumeID = "Nano11Go"

	boolFields := map[string]*bool{
		"skip_appx":             &d.flags.skipAppx,
		"skip_packages":         &d.flags.skipPackages,
		"skip_file_cleanup":     &d.flags.skipFileCleanup,
		"skip_winsxs_wipe":      &d.flags.skipWinSxSWipe,
		"skip_reg_tweaks":       &d.flags.skipRegTweaks,
		"skip_services":         &d.flags.skipServices,
		"keep_nic_drivers":      &d.flags.keepNICDrivers,
		"keep_drivers":          &d.flags.keepDrivers,
		"remove_web_engines":    &d.flags.removeWebEngines,
		"keep_defender_search":  &d.flags.keepDefenderSearch,
		"remove_ai":             &d.flags.removeAI,
		"remove_store_apps":     &d.flags.removeStoreApps,
		"remove_uwp_frameworks": &d.flags.removeUWPFrameworks,
		"remove_ai_foundation":  &d.flags.removeAIFoundation,
		"remove_ime":            &d.flags.removeIME,
		"keep_iso_extras":       &d.flags.keepISOExtras,
		"skip_iso_autounattend": &d.flags.skipISOAutounattend,
	}
	stringFields := map[string]*string{
		"nano11go_binary": &d.nano11goBinary,
		"lzx_preset":      &d.lzxPreset,
		"iso_volume_id":   &d.isoVolumeID,
	}

	for _, raw := range configs {
		cval, ok := raw.(cty.Value)
		if !ok || cval.IsNull() || !cval.IsKnown() {
			continue
		}
		if v := cval.GetAttr("iso_path"); v.IsKnown() && !v.IsNull() {
			d.isoPath = v.AsString()
		}
		for name, ptr := range stringFields {
			if v := cval.GetAttr(name); v.IsKnown() && !v.IsNull() && v.AsString() != "" {
				*ptr = v.AsString()
			}
		}
		if v := cval.GetAttr("image_index"); v.IsKnown() && !v.IsNull() {
			n, _ := v.AsBigFloat().Int64()
			d.imageIndex = int(n)
		}
		for name, ptr := range boolFields {
			if v := cval.GetAttr(name); v.IsKnown() && !v.IsNull() {
				*ptr = v.True()
			}
		}
		if v := cval.GetAttr("registry_tweaks"); v.IsKnown() && !v.IsNull() {
			d.registryTweaks = nil
			for _, tv := range v.AsValueSlice() {
				tweak, err := parseRegistryTweak(tv)
				if err != nil {
					return err
				}
				d.registryTweaks = append(d.registryTweaks, tweak)
			}
		}
	}

	if d.isoPath == "" {
		return fmt.Errorf("iso_path is required")
	}
	if d.imageIndex < 1 {
		return fmt.Errorf("image_index must be >= 1")
	}
	for _, t := range d.registryTweaks {
		if err := validateRegistryTweak(t); err != nil {
			return err
		}
	}
	if _, err := exec.LookPath(d.nano11goBinary); err != nil {
		return fmt.Errorf("nano11go_binary %q not found: %w (build/install nano11-go and put it on PATH, or set nano11go_binary to its path)", d.nano11goBinary, err)
	}

	return nil
}

func parseRegistryTweak(v cty.Value) (RegistryTweak, error) {
	var t RegistryTweak
	if v.IsNull() || !v.IsKnown() {
		return t, fmt.Errorf("registry_tweaks: entry must not be null/unknown")
	}
	get := func(name string) string {
		attr := v.GetAttr(name)
		if attr.IsNull() || !attr.IsKnown() {
			return ""
		}
		return attr.AsString()
	}
	t.Hive = get("hive")
	t.Path = get("path")
	t.Name = get("name")
	t.Type = get("type")
	t.Value = get("value")
	return t, validateRegistryTweak(t)
}

func validateRegistryTweak(t RegistryTweak) error {
	if t.Hive == "" {
		return fmt.Errorf("registry_tweaks: hive is required")
	}
	if !isKnownHive(t.Hive) {
		return fmt.Errorf("registry_tweaks: unknown hive %q (want one of SYSTEM, SOFTWARE, DEFAULT, SAM, COMPONENTS, NTUSER.DAT)", t.Hive)
	}
	if t.Path == "" {
		return fmt.Errorf("registry_tweaks: path is required")
	}
	if t.Name == "" {
		return fmt.Errorf("registry_tweaks: name is required")
	}
	switch t.Type {
	case "dword":
		if _, err := strconv.ParseUint(t.Value, 0, 32); err != nil {
			return fmt.Errorf("registry_tweaks: value %q is not a valid dword: %w", t.Value, err)
		}
	case "string":
		// any value is valid
	default:
		return fmt.Errorf("registry_tweaks: unknown type %q (want \"dword\" or \"string\")", t.Type)
	}
	return nil
}

func (d *Datasource) Execute() (cty.Value, error) {
	path, err := debloatISO(d)
	if err != nil {
		return cty.NilVal, err
	}
	return cty.ObjectVal(map[string]cty.Value{
		"debloated_iso_path": cty.StringVal(path),
	}), nil
}
