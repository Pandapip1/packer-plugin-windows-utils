package debloat

import (
	"strings"
	"testing"

	nano11go "github.com/Pandapip1/nano11-go"

	"github.com/Pandapip1/gowim/lzx"
)

func TestDebloatOptionsDefaults(t *testing.T) {
	d := &Datasource{imageIndex: 1, lzxPreset: "fast"}
	got, err := debloatOptions(d)
	if err != nil {
		t.Fatalf("debloatOptions: %v", err)
	}
	want := nano11go.DebloatOptions{WinRE: nano11go.WinREDonorStub, LZX: lzx.Fast()}
	if got != want {
		t.Fatalf("debloatOptions() = %+v, want %+v", got, want)
	}
}

func TestDebloatOptionsMapsSetFlags(t *testing.T) {
	d := &Datasource{
		imageIndex: 2,
		lzxPreset:  "max",
		flags: nano11Flags{
			skipAppx:        true,
			keepNICDrivers:  true,
			removeAI:        true,
			skipWinSxSWipe:  true,
			removeStoreApps: false,
		},
	}
	got, err := debloatOptions(d)
	if err != nil {
		t.Fatalf("debloatOptions: %v", err)
	}
	if !got.SkipAppx || !got.KeepNICDrivers || !got.RemoveAI || !got.SkipWinSxSWipe {
		t.Fatalf("debloatOptions() = %+v, expected those four fields true", got)
	}
	if got.RemoveStoreApps || got.SkipPackages || got.KeepDrivers {
		t.Fatalf("debloatOptions() = %+v, expected those three fields false", got)
	}
	if got.WinRE != nano11go.WinREDonorStub {
		t.Fatalf("debloatOptions() WinRE = %v, want WinREDonorStub (nano11-go CLI's own default)", got.WinRE)
	}
}

func TestDebloatOptionsKeepDriversImpliesKeepNICDrivers(t *testing.T) {
	d := &Datasource{lzxPreset: "fast", flags: nano11Flags{keepDrivers: true}}
	got, err := debloatOptions(d)
	if err != nil {
		t.Fatalf("debloatOptions: %v", err)
	}
	if !got.KeepDrivers || !got.KeepNICDrivers {
		t.Fatalf("debloatOptions() = %+v, expected KeepDrivers and KeepNICDrivers both true (matches nano11-go CLI's own implication)", got)
	}
}

func TestDebloatOptionsRejectsInvalidLZXPreset(t *testing.T) {
	d := &Datasource{lzxPreset: "ludicrous-speed"}
	_, err := debloatOptions(d)
	if err == nil || !strings.Contains(err.Error(), "invalid lzx_preset") {
		t.Fatalf("expected invalid lzx_preset error, got %v", err)
	}
}

func TestLZXPresetOptionsAcceptsAllPresets(t *testing.T) {
	for _, name := range []string{"fast", "balanced", "default", "max", "none"} {
		if _, err := lzxPresetOptions(name); err != nil {
			t.Fatalf("lzxPresetOptions(%q): %v", name, err)
		}
	}
}

func TestISOOptions(t *testing.T) {
	d := &Datasource{isoVolumeID: "MyVol"}
	got := isoOptions(d, "/media", "/tmp/install.wim", "/tmp/out.iso")
	want := nano11go.ISOOptions{
		Dir:        "/media",
		Out:        "/tmp/out.iso",
		VolID:      "MyVol",
		InstallWim: "/tmp/install.wim",
	}
	if got != want {
		t.Fatalf("isoOptions() = %+v, want %+v", got, want)
	}
}

func TestISOOptionsWithFlags(t *testing.T) {
	d := &Datasource{
		isoVolumeID: "MyVol",
		flags: nano11Flags{
			keepISOExtras:       true,
			skipISOAutounattend: true,
		},
	}
	got := isoOptions(d, "/media", "/tmp/install.wim", "/tmp/out.iso")
	if !got.KeepExtras || !got.SkipAutounattend {
		t.Fatalf("isoOptions() = %+v, expected KeepExtras and SkipAutounattend both true", got)
	}
}
