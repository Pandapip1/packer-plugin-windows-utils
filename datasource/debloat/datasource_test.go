package debloat

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/zclconf/go-cty/cty"
)

// tweakVal builds a cty.Value for one registry_tweaks entry.
func tweakVal(hive, path, name, typ, value string) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"hive":  cty.StringVal(hive),
		"path":  cty.StringVal(path),
		"name":  cty.StringVal(name),
		"type":  cty.StringVal(typ),
		"value": cty.StringVal(value),
	})
}

// configVal builds a cty.Value matching (*Datasource).ConfigSpec()'s full
// object type -- every attribute present, null unless overridden -- exactly
// as hcldec.Decode would hand Configure a real HCL block's fields
// (including every field the block itself left unset, as null). Building
// test input any other way (a bare cty.ObjectVal with only the attributes a
// given test cares about) produces an object type Configure's
// cval.GetAttr calls will panic on, since GetAttr requires the attribute to
// exist in the value's type even when null.
func configVal(overrides map[string]cty.Value) cty.Value {
	spec := (&Datasource{}).ConfigSpec()
	attrs := make(map[string]cty.Value, len(spec))
	for name, s := range spec {
		as, ok := s.(*hcldec.AttrSpec)
		if !ok {
			continue
		}
		if v, ok := overrides[name]; ok {
			attrs[name] = v
			continue
		}
		attrs[name] = cty.NullVal(as.Type)
	}
	return cty.ObjectVal(attrs)
}

func TestConfigureDefaultsMatchNano11GoCLI(t *testing.T) {
	d := &Datasource{}
	err := d.Configure(configVal(map[string]cty.Value{
		"iso_path":        cty.StringVal("/tmp/whatever.iso"),
		"nano11go_binary": cty.StringVal("go"), // just needs to resolve via exec.LookPath
	}))
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	// Leaving everything but iso_path unset must reproduce nano11-go's own
	// CLI defaults exactly (see nano11Flags' doc comment).
	if d.flags != (nano11Flags{}) {
		t.Fatalf("expected all stage flags to default to false, got %+v", d.flags)
	}
	if d.imageIndex != 1 {
		t.Fatalf("expected default image_index 1, got %d", d.imageIndex)
	}
	if d.lzxPreset != "fast" {
		t.Fatalf("expected default lzx_preset \"fast\", got %q", d.lzxPreset)
	}
	if d.isoVolumeID != "Nano11Go" {
		t.Fatalf("expected default iso_volume_id \"Nano11Go\", got %q", d.isoVolumeID)
	}
}

func TestConfigureRequiresIsoPath(t *testing.T) {
	d := &Datasource{}
	err := d.Configure(configVal(map[string]cty.Value{
		"nano11go_binary": cty.StringVal("go"),
	}))
	if err == nil || !strings.Contains(err.Error(), "iso_path is required") {
		t.Fatalf("expected iso_path required error, got %v", err)
	}
}

func TestConfigureMapsBoolFlags(t *testing.T) {
	d := &Datasource{}
	err := d.Configure(configVal(map[string]cty.Value{
		"iso_path":              cty.StringVal("/tmp/whatever.iso"),
		"nano11go_binary":       cty.StringVal("go"),
		"skip_appx":             cty.BoolVal(true),
		"keep_nic_drivers":      cty.BoolVal(true),
		"remove_uwp_frameworks": cty.BoolVal(true),
		"keep_iso_extras":       cty.BoolVal(true),
	}))
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if !d.flags.skipAppx || !d.flags.keepNICDrivers || !d.flags.removeUWPFrameworks || !d.flags.keepISOExtras {
		t.Fatalf("expected the four set bool fields to be true, got %+v", d.flags)
	}
	if d.flags.skipPackages || d.flags.removeAI {
		t.Fatalf("expected unset bool fields to remain false, got %+v", d.flags)
	}
}

func TestConfigureRejectsInvalidImageIndex(t *testing.T) {
	d := &Datasource{}
	err := d.Configure(configVal(map[string]cty.Value{
		"iso_path":        cty.StringVal("/tmp/whatever.iso"),
		"nano11go_binary": cty.StringVal("go"),
		"image_index":     cty.NumberIntVal(0),
	}))
	if err == nil || !strings.Contains(err.Error(), "image_index") {
		t.Fatalf("expected image_index validation error, got %v", err)
	}
}

func TestConfigureRejectsMissingBinary(t *testing.T) {
	d := &Datasource{}
	err := d.Configure(configVal(map[string]cty.Value{
		"iso_path":        cty.StringVal("/tmp/whatever.iso"),
		"nano11go_binary": cty.StringVal("nano11-go-definitely-not-on-path-xyz"),
	}))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected binary-not-found error, got %v", err)
	}
}

func TestConfigureParsesRegistryTweaks(t *testing.T) {
	d := &Datasource{}
	err := d.Configure(configVal(map[string]cty.Value{
		"iso_path":        cty.StringVal("/tmp/whatever.iso"),
		"nano11go_binary": cty.StringVal("go"),
		"registry_tweaks": cty.ListVal([]cty.Value{
			tweakVal("SOFTWARE", `Microsoft\Windows\CurrentVersion\Policies\System`, "LocalAccountTokenFilterPolicy", "dword", "1"),
			tweakVal("SOFTWARE", `Some\Path`, "SomeString", "string", "hello"),
		}),
	}))
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if len(d.registryTweaks) != 2 {
		t.Fatalf("expected 2 registry tweaks, got %d", len(d.registryTweaks))
	}
	if d.registryTweaks[0].Name != "LocalAccountTokenFilterPolicy" || d.registryTweaks[0].Value != "1" {
		t.Fatalf("unexpected tweak[0]: %+v", d.registryTweaks[0])
	}
	if d.registryTweaks[1].Type != "string" || d.registryTweaks[1].Value != "hello" {
		t.Fatalf("unexpected tweak[1]: %+v", d.registryTweaks[1])
	}
}

func TestConfigureRejectsBadRegistryTweaks(t *testing.T) {
	cases := []struct {
		name  string
		tweak cty.Value
		want  string
	}{
		{"unknown hive", tweakVal("NOTAHIVE", `A\B`, "N", "dword", "1"), "unknown hive"},
		{"missing path", tweakVal("SOFTWARE", "", "N", "dword", "1"), "path is required"},
		{"missing name", tweakVal("SOFTWARE", `A\B`, "", "dword", "1"), "name is required"},
		{"bad dword", tweakVal("SOFTWARE", `A\B`, "N", "dword", "not-a-number"), "not a valid dword"},
		{"unknown type", tweakVal("SOFTWARE", `A\B`, "N", "float", "1"), "unknown type"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &Datasource{}
			err := d.Configure(configVal(map[string]cty.Value{
				"iso_path":        cty.StringVal("/tmp/whatever.iso"),
				"nano11go_binary": cty.StringVal("go"),
				"registry_tweaks": cty.ListVal([]cty.Value{c.tweak}),
			}))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("expected error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestValidateRegistryTweakAcceptsHexDword(t *testing.T) {
	if err := validateRegistryTweak(RegistryTweak{Hive: "SYSTEM", Path: `A\B`, Name: "N", Type: "dword", Value: "0x1"}); err != nil {
		t.Fatalf("expected hex dword to validate, got %v", err)
	}
}
