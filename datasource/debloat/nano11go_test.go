package debloat

import (
	"reflect"
	"testing"
)

func TestDebloatArgsDefaults(t *testing.T) {
	d := &Datasource{imageIndex: 1, lzxPreset: "fast"}
	got := debloatArgs(d, "in.wim", "out.wim")
	want := []string{
		"-wim", "in.wim",
		"-out", "out.wim",
		"-image", "1",
		"-lzx-preset", "fast",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("debloatArgs() = %v, want %v", got, want)
	}
}

func TestDebloatArgsIncludesSetFlags(t *testing.T) {
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
	got := debloatArgs(d, "in.wim", "out.wim")
	for _, want := range []string{"-skip-appx", "-keep-nic-drivers", "-remove-ai", "-skip-winsxs-wipe"} {
		if !contains(got, want) {
			t.Fatalf("debloatArgs() = %v, missing %q", got, want)
		}
	}
	for _, unwanted := range []string{"-remove-store-apps", "-skip-packages", "-keep-drivers"} {
		if contains(got, unwanted) {
			t.Fatalf("debloatArgs() = %v, should not contain %q", got, unwanted)
		}
	}
}

func TestAuthorISOArgs(t *testing.T) {
	d := &Datasource{isoVolumeID: "MyVol"}
	got := authorISOArgs(d, "/media", "/tmp/install.wim", "/tmp/out.iso")
	want := []string{
		"-iso-dir", "/media",
		"-iso-out", "/tmp/out.iso",
		"-iso-install-wim", "/tmp/install.wim",
		"-iso-volid", "MyVol",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authorISOArgs() = %v, want %v", got, want)
	}
}

func TestAuthorISOArgsWithOptions(t *testing.T) {
	d := &Datasource{
		isoVolumeID: "MyVol",
		flags: nano11Flags{
			keepISOExtras:       true,
			skipISOAutounattend: true,
		},
	}
	got := authorISOArgs(d, "/media", "/tmp/install.wim", "/tmp/out.iso")
	for _, want := range []string{"-keep-iso-extras", "-skip-iso-autounattend"} {
		if !contains(got, want) {
			t.Fatalf("authorISOArgs() = %v, missing %q", got, want)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
