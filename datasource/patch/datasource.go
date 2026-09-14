package patch

import (
	"fmt"
	"strconv"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/zclconf/go-cty/cty"
)

// registryTweakType is the cty object type of one entry in registry_tweaks,
// matching the debloat datasource's own registryTweakType 1:1 (kept
// separate since the two datasources share no HCL-facing dependency).
var registryTweakType = cty.Object(map[string]cty.Type{
	"hive":  cty.String,
	"path":  cty.String,
	"name":  cty.String,
	"type":  cty.String,
	"value": cty.String,
})

type Datasource struct {
	isoPath            string
	patchNoBoot        bool
	extraDrivers       []string
	registryTweaks     []RegistryTweak
	imageIndex         int
	enableSSH          bool
	disableScreenBlank bool
}

func (d *Datasource) ConfigSpec() hcldec.ObjectSpec {
	return hcldec.ObjectSpec{
		"iso_path":        &hcldec.AttrSpec{Name: "iso_path", Type: cty.String, Required: true},
		"patch_noboot":    &hcldec.AttrSpec{Name: "patch_noboot", Type: cty.Bool, Required: false},
		"extra_drivers":   &hcldec.AttrSpec{Name: "extra_drivers", Type: cty.List(cty.String), Required: false},
		"registry_tweaks": &hcldec.AttrSpec{Name: "registry_tweaks", Type: cty.List(registryTweakType), Required: false},
		// image_index selects which image inside a multi-edition
		// sources/install.wim registry_tweaks (and enable_ssh) apply to
		// (1-based, matching Windows WIM image indices -- e.g. the same
		// value passed as /IMAGE/INDEX in Autounattend.xml). Ignored when
		// registry_tweaks is empty and enable_ssh is false.
		"image_index": &hcldec.AttrSpec{Name: "image_index", Type: cty.Number, Required: false},
		// enable_ssh bakes the OpenSSH.Server capability into
		// sources/install.wim fully offline (component.Install, no
		// Add-WindowsCapability/WU Agent involved -- see openssh.go), plus
		// the registry tweaks needed for it to actually come up at first
		// boot with zero specialize-pass scripts: sshd's service Start type,
		// SOFTWARE\OpenSSH!DefaultShell, LocalAccountTokenFilterPolicy, and
		// an inbound TCP/22 Windows Firewall allow rule. See
		// opensshRegistryTweaks' doc comment for the exact runtime-script
		// equivalent this replaces.
		"enable_ssh": &hcldec.AttrSpec{Name: "enable_ssh", Type: cty.Bool, Required: false},
		// disable_screen_blank bakes in a fixed set of registry tweaks (see
		// screenBlankRegistryTweaks in screenblank.go) that disable the
		// screensaver and the display/monitor power-off timeout, so a
		// screenshot-driven console/VNC session never finds the screen
		// blanked. Equivalent to hand-writing those same registry_tweaks
		// entries yourself; provided as a named toggle since this is a
		// common, easy-to-get-wrong headless-automation need.
		"disable_screen_blank": &hcldec.AttrSpec{Name: "disable_screen_blank", Type: cty.Bool, Required: false},
	}
}

func (d *Datasource) OutputSpec() hcldec.ObjectSpec {
	return hcldec.ObjectSpec{
		"patched_iso_path": &hcldec.AttrSpec{Name: "patched_iso_path", Type: cty.String},
	}
}

func (d *Datasource) Configure(configs ...interface{}) error {
	d.patchNoBoot = true
	d.imageIndex = 1
	for _, raw := range configs {
		cval, ok := raw.(cty.Value)
		if !ok || cval.IsNull() || !cval.IsKnown() {
			continue
		}
		if v := cval.GetAttr("iso_path"); v.IsKnown() && !v.IsNull() {
			d.isoPath = v.AsString()
		}
		if v := cval.GetAttr("patch_noboot"); v.IsKnown() && !v.IsNull() {
			d.patchNoBoot = v.True()
		}
		if v := cval.GetAttr("extra_drivers"); v.IsKnown() && !v.IsNull() {
			d.extraDrivers = nil
			for _, ev := range v.AsValueSlice() {
				if ev.IsKnown() && !ev.IsNull() {
					d.extraDrivers = append(d.extraDrivers, ev.AsString())
				}
			}
		}
		if v := cval.GetAttr("image_index"); v.IsKnown() && !v.IsNull() {
			n, _ := v.AsBigFloat().Int64()
			d.imageIndex = int(n)
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
		if v := cval.GetAttr("enable_ssh"); v.IsKnown() && !v.IsNull() {
			d.enableSSH = v.True()
		}
		if v := cval.GetAttr("disable_screen_blank"); v.IsKnown() && !v.IsNull() {
			d.disableScreenBlank = v.True()
		}
	}
	if d.isoPath == "" {
		return fmt.Errorf("iso_path is required")
	}
	if d.imageIndex < 1 {
		return fmt.Errorf("image_index must be >= 1")
	}
	if d.disableScreenBlank {
		d.registryTweaks = append(d.registryTweaks, screenBlankRegistryTweaks()...)
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
	if !IsKnownHive(t.Hive) {
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
	path, err := patchISO(d.isoPath, d.patchNoBoot, d.extraDrivers, d.registryTweaks, d.imageIndex, d.enableSSH)
	if err != nil {
		return cty.NilVal, err
	}
	return cty.ObjectVal(map[string]cty.Value{
		"patched_iso_path": cty.StringVal(path),
	}), nil
}
