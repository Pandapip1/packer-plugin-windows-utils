package patch

import (
	"bytes"
	"crypto/sha1"
	"os"
	"testing"

	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/registry"
	"github.com/Pandapip1/gowim/wim"
)

// buildTestWimWithDefaultUserHive writes a single-image install.wim
// containing empty SYSTEM, SOFTWARE, and Users\Default\NTUSER.DAT hives, so
// screenBlankRegistryTweaks' writes into HiveDefaultUser (the "Default User"
// profile template every brand-new local profile's HKEY_CURRENT_USER is
// seeded from at first logon) can be exercised the same way
// regtweaks_test.go's buildTestMultiImageWim exercises SYSTEM/SOFTWARE.
func buildTestWimWithDefaultUserHive(t *testing.T) string {
	t.Helper()

	emptyHive := func() []byte {
		hive := &regf.Hive{
			BaseBlock: regf.BaseBlock{
				MajorVersion:     1,
				MinorVersion:     regf.Version1_5,
				FileType:         regf.FileTypePrimary,
				ClusteringFactor: 1,
			},
			Root: &regf.Key{Flags: regf.KeyFlagHiveEntry},
		}
		data, err := hive.AppendTo(nil)
		if err != nil {
			t.Fatalf("buildTestWimWithDefaultUserHive: hive AppendTo: %v", err)
		}
		return data
	}

	bt := &wim.BlobTable{}
	src := wim.MapBlobSource{}
	root := &wim.DirEntry{Attributes: wim.FileAttributeDirectory, SecurityID: wim.SecurityIDNone}
	for _, path := range []string{
		`Windows\System32\config\SYSTEM`,
		`Windows\System32\config\SOFTWARE`,
		`Users\Default\NTUSER.DAT`,
	} {
		data := emptyHive()
		hash := wim.Hash(sha1.Sum(data))
		bt.Entries = append(bt.Entries, wim.BlobDescriptor{Hash: hash, PartNumber: 1, RefCount: 1})
		src[hash] = data
		if _, err := root.Add(path, hash); err != nil {
			t.Fatalf("buildTestWimWithDefaultUserHive: Add(%s): %v", path, err)
		}
	}
	images := []*wim.ImageMetadata{{Security: &wim.SecurityData{}, Root: root}}

	wimBytes, err := wim.Assemble(images, bt, &wim.XMLData{}, src, wim.WriteOptions{GUID: wim.GUID{1}})
	if err != nil {
		t.Fatalf("buildTestWimWithDefaultUserHive: Assemble: %v", err)
	}

	f, err := os.CreateTemp("", "patch-screenblank-test-*.wim")
	if err != nil {
		t.Fatalf("buildTestWimWithDefaultUserHive: CreateTemp: %v", err)
	}
	path := f.Name()
	if _, err := f.Write(wimBytes); err != nil {
		t.Fatalf("buildTestWimWithDefaultUserHive: Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("buildTestWimWithDefaultUserHive: Close: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	return path
}

func TestScreenBlankRegistryTweaksApplyCorrectly(t *testing.T) {
	wimPath := buildTestWimWithDefaultUserHive(t)

	tweaks := screenBlankRegistryTweaks()
	if err := applyRegistryTweaksToWim(wimPath, 1, tweaks); err != nil {
		t.Fatalf("applyRegistryTweaksToWim: %v", err)
	}

	data, err := os.ReadFile(wimPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	r, err := wim.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	bt, err := r.BlobTable()
	if err != nil {
		t.Fatalf("BlobTable: %v", err)
	}
	metas := bt.MetadataResources()
	meta, err := r.ImageMetadata(metas[0])
	if err != nil {
		t.Fatalf("ImageMetadata: %v", err)
	}
	hs, err := registry.LoadHiveSet(r, meta.Root, bt)
	if err != nil {
		t.Fatalf("LoadHiveSet: %v", err)
	}

	getValue := func(hiveName, keyPath, valueName string) *regf.Value {
		h, ok := hs.Hives[hiveName]
		if !ok {
			t.Fatalf("hive %s not loaded", hiveName)
		}
		key := h.Hive.Root.OpenPath(keyPath)
		if key == nil {
			return nil
		}
		return key.Value(valueName)
	}

	// Screensaver: written into the Default User profile template
	// (HiveDefaultUser), not into HKLM, since the screensaver's own Group
	// Policy ADMX definitions (CPL_Personalization_EnableScreenSaver /
	// CPL_Personalization_ScreenSaverTimeOut) are class="User" only -- there
	// is no supported machine-wide policy equivalent for this one.
	active := getValue(registry.HiveDefaultUser, `Control Panel\Desktop`, "ScreenSaveActive")
	if active == nil {
		t.Fatalf("expected ScreenSaveActive to be set in the Default User hive")
	}
	if s := active.SZ(); s != "0" {
		t.Fatalf("ScreenSaveActive = %q; want \"0\"", s)
	}

	timeout := getValue(registry.HiveDefaultUser, `Control Panel\Desktop`, "ScreenSaveTimeOut")
	if timeout == nil {
		t.Fatalf("expected ScreenSaveTimeOut to be set in the Default User hive")
	}
	if s := timeout.SZ(); s != "0" {
		t.Fatalf("ScreenSaveTimeOut = %q; want \"0\"", s)
	}

	// Display power-off: written as the class="Machine" Group Policy
	// equivalent (HKLM\SOFTWARE\Policies\Microsoft\Power\PowerSettings\
	// <VIDEOIDLE guid>), which -- unlike baking a value directly into a
	// specific power scheme's own GUID-keyed tree -- applies regardless of
	// which scheme ends up active at first boot.
	for _, valueName := range []string{"ACSettingIndex", "DCSettingIndex"} {
		v := getValue(registry.HiveSoftware, `Policies\Microsoft\Power\PowerSettings\`+screenSaverOffTimeoutGUID, valueName)
		if v == nil {
			t.Fatalf("expected %s to be set under the PowerSettings policy key", valueName)
		}
		n, err := v.DWORD()
		if err != nil {
			t.Fatalf("%s: DWORD: %v", valueName, err)
		}
		if n != 0 {
			t.Fatalf("%s = %d; want 0 (never)", valueName, n)
		}
	}
}
