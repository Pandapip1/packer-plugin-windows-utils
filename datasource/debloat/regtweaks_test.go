package debloat

import (
	"bytes"
	"crypto/sha1"
	"os"
	"testing"

	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/registry"
	"github.com/Pandapip1/gowim/wim"
)

// buildTestInstallWim writes a minimal, single-image install.wim to a temp
// file containing empty SYSTEM/SOFTWARE/DEFAULT/NTUSER.DAT hives at their
// standard image-relative paths, mirroring gowim/registry's own
// registry_test.go buildTestImage fixture approach (a real
// serialized-and-reparsed WIM, not a hand-rolled stand-in) so
// applyRegistryTweaksToWim is exercised against the exact same code paths a
// real install.wim would hit.
func buildTestInstallWim(t *testing.T) string {
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
			t.Fatalf("buildTestInstallWim: hive AppendTo: %v", err)
		}
		return data
	}

	files := map[string][]byte{
		`Windows\System32\config\SYSTEM`:   emptyHive(),
		`Windows\System32\config\SOFTWARE`: emptyHive(),
		`Windows\System32\config\DEFAULT`:  emptyHive(),
		`Users\Default\NTUSER.DAT`:         emptyHive(),
	}

	bt := &wim.BlobTable{}
	src := wim.MapBlobSource{}
	root := &wim.DirEntry{Attributes: wim.FileAttributeDirectory, SecurityID: wim.SecurityIDNone}
	for path, data := range files {
		hash := wim.Hash(sha1.Sum(data))
		bt.Entries = append(bt.Entries, wim.BlobDescriptor{Hash: hash, PartNumber: 1, RefCount: 1})
		src[hash] = data
		if _, err := root.Add(path, hash); err != nil {
			t.Fatalf("buildTestInstallWim: Add(%s): %v", path, err)
		}
	}
	images := []*wim.ImageMetadata{{Security: &wim.SecurityData{}, Root: root}}

	wimBytes, err := wim.Assemble(images, bt, &wim.XMLData{}, src, wim.WriteOptions{GUID: wim.GUID{1}})
	if err != nil {
		t.Fatalf("buildTestInstallWim: Assemble: %v", err)
	}

	f, err := os.CreateTemp("", "regtweaks-test-*.wim")
	if err != nil {
		t.Fatalf("buildTestInstallWim: CreateTemp: %v", err)
	}
	path := f.Name()
	if _, err := f.Write(wimBytes); err != nil {
		t.Fatalf("buildTestInstallWim: Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("buildTestInstallWim: Close: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	return path
}

// readHiveValue reopens wimPath and returns the named value from the given
// hive/path, or nil if not found.
func readHiveValue(t *testing.T, wimPath, hiveName, keyPath, valueName string) *regf.Value {
	t.Helper()
	data, err := os.ReadFile(wimPath)
	if err != nil {
		t.Fatalf("readHiveValue: ReadFile: %v", err)
	}
	r, err := wim.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("readHiveValue: NewReader: %v", err)
	}
	bt, err := r.BlobTable()
	if err != nil {
		t.Fatalf("readHiveValue: BlobTable: %v", err)
	}
	metas := bt.MetadataResources()
	if len(metas) != 1 {
		t.Fatalf("readHiveValue: got %d image metadata resources, want 1", len(metas))
	}
	meta, err := r.ImageMetadata(metas[0])
	if err != nil {
		t.Fatalf("readHiveValue: ImageMetadata: %v", err)
	}
	hs, err := registry.LoadHiveSet(r, meta.Root, bt)
	if err != nil {
		t.Fatalf("readHiveValue: LoadHiveSet: %v", err)
	}
	h, ok := hs.Hives[hiveName]
	if !ok {
		t.Fatalf("readHiveValue: hive %s not loaded", hiveName)
	}
	key := h.Hive.Root.OpenPath(keyPath)
	if key == nil {
		return nil
	}
	return key.Value(valueName)
}

func TestApplyRegistryTweaksToWimDword(t *testing.T) {
	wimPath := buildTestInstallWim(t)

	tweaks := []RegistryTweak{
		{
			Hive:  registry.HiveSoftware,
			Path:  `Microsoft\Windows\CurrentVersion\Policies\System`,
			Name:  "LocalAccountTokenFilterPolicy",
			Type:  "dword",
			Value: "1",
		},
	}
	if err := applyRegistryTweaksToWim(wimPath, tweaks); err != nil {
		t.Fatalf("applyRegistryTweaksToWim: %v", err)
	}

	v := readHiveValue(t, wimPath, registry.HiveSoftware, `Microsoft\Windows\CurrentVersion\Policies\System`, "LocalAccountTokenFilterPolicy")
	if v == nil {
		t.Fatalf("expected LocalAccountTokenFilterPolicy to be set")
	}
	n, err := v.DWORD()
	if err != nil {
		t.Fatalf("DWORD: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected value 1, got %d", n)
	}
}

func TestApplyRegistryTweaksToWimString(t *testing.T) {
	wimPath := buildTestInstallWim(t)

	tweaks := []RegistryTweak{
		{
			Hive:  registry.HiveSystem,
			Path:  `Setup\SomeKey`,
			Name:  "SomeValue",
			Type:  "string",
			Value: "hello world",
		},
	}
	if err := applyRegistryTweaksToWim(wimPath, tweaks); err != nil {
		t.Fatalf("applyRegistryTweaksToWim: %v", err)
	}

	v := readHiveValue(t, wimPath, registry.HiveSystem, `Setup\SomeKey`, "SomeValue")
	if v == nil {
		t.Fatalf("expected SomeValue to be set")
	}
	if got := v.SZ(); got != "hello world" {
		t.Fatalf("expected \"hello world\", got %q", got)
	}
}

func TestApplyRegistryTweaksToWimUnknownHive(t *testing.T) {
	wimPath := buildTestInstallWim(t)
	// SAM/COMPONENTS were not included in the test fixture, so this hive is
	// legitimately absent from the loaded HiveSet.
	err := applyRegistryTweaksToWim(wimPath, []RegistryTweak{{
		Hive: registry.HiveSAM, Path: `A\B`, Name: "N", Type: "dword", Value: "1",
	}})
	if err == nil {
		t.Fatalf("expected an error for a hive absent from the image")
	}
}
