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

// buildTestMultiImageWim writes a two-image install.wim (mirroring stock
// Windows media, which always ships every edition in one multi-image
// install.wim) to a temp file, each image containing its own empty
// SYSTEM/SOFTWARE hives, so applyRegistryTweaksToWim's imageIndex selection
// and its preservation of every *other* image can both be exercised against
// a real serialized-and-reparsed WIM.
func buildTestMultiImageWim(t *testing.T) string {
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
			t.Fatalf("buildTestMultiImageWim: hive AppendTo: %v", err)
		}
		return data
	}

	bt := &wim.BlobTable{}
	src := wim.MapBlobSource{}
	var images []*wim.ImageMetadata
	for i := 0; i < 2; i++ {
		root := &wim.DirEntry{Attributes: wim.FileAttributeDirectory, SecurityID: wim.SecurityIDNone}
		for _, path := range []string{`Windows\System32\config\SYSTEM`, `Windows\System32\config\SOFTWARE`} {
			data := emptyHive()
			hash := wim.Hash(sha1.Sum(data))
			bt.Entries = append(bt.Entries, wim.BlobDescriptor{Hash: hash, PartNumber: 1, RefCount: 1})
			src[hash] = data
			if _, err := root.Add(path, hash); err != nil {
				t.Fatalf("buildTestMultiImageWim: Add(%s) image %d: %v", path, i+1, err)
			}
		}
		images = append(images, &wim.ImageMetadata{Security: &wim.SecurityData{}, Root: root})
	}

	wimBytes, err := wim.Assemble(images, bt, &wim.XMLData{}, src, wim.WriteOptions{GUID: wim.GUID{1}})
	if err != nil {
		t.Fatalf("buildTestMultiImageWim: Assemble: %v", err)
	}

	f, err := os.CreateTemp("", "patch-regtweaks-test-*.wim")
	if err != nil {
		t.Fatalf("buildTestMultiImageWim: CreateTemp: %v", err)
	}
	path := f.Name()
	if _, err := f.Write(wimBytes); err != nil {
		t.Fatalf("buildTestMultiImageWim: Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("buildTestMultiImageWim: Close: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	return path
}

// readHiveValueFromImage reopens wimPath and returns the named value from
// the given hive/path within the imageIndex'th (1-based) image, or nil if
// not found.
func readHiveValueFromImage(t *testing.T, wimPath string, imageIndex int, hiveName, keyPath, valueName string) *regf.Value {
	t.Helper()
	data, err := os.ReadFile(wimPath)
	if err != nil {
		t.Fatalf("readHiveValueFromImage: ReadFile: %v", err)
	}
	r, err := wim.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("readHiveValueFromImage: NewReader: %v", err)
	}
	bt, err := r.BlobTable()
	if err != nil {
		t.Fatalf("readHiveValueFromImage: BlobTable: %v", err)
	}
	metas := bt.MetadataResources()
	if imageIndex < 1 || imageIndex > len(metas) {
		t.Fatalf("readHiveValueFromImage: image %d out of range (%d images)", imageIndex, len(metas))
	}
	meta, err := r.ImageMetadata(metas[imageIndex-1])
	if err != nil {
		t.Fatalf("readHiveValueFromImage: ImageMetadata: %v", err)
	}
	hs, err := registry.LoadHiveSet(r, meta.Root, bt)
	if err != nil {
		t.Fatalf("readHiveValueFromImage: LoadHiveSet: %v", err)
	}
	h, ok := hs.Hives[hiveName]
	if !ok {
		t.Fatalf("readHiveValueFromImage: hive %s not loaded", hiveName)
	}
	key := h.Hive.Root.OpenPath(keyPath)
	if key == nil {
		return nil
	}
	return key.Value(valueName)
}

func TestApplyRegistryTweaksToWimSelectsCorrectImage(t *testing.T) {
	wimPath := buildTestMultiImageWim(t)

	tweaks := []RegistryTweak{
		{
			Hive:  registry.HiveSoftware,
			Path:  `Policies\Microsoft\Windows Defender`,
			Name:  "DisableAntiSpyware",
			Type:  "dword",
			Value: "1",
		},
	}
	if err := applyRegistryTweaksToWim(wimPath, 2, tweaks); err != nil {
		t.Fatalf("applyRegistryTweaksToWim: %v", err)
	}

	v := readHiveValueFromImage(t, wimPath, 2, registry.HiveSoftware, `Policies\Microsoft\Windows Defender`, "DisableAntiSpyware")
	if v == nil {
		t.Fatalf("expected DisableAntiSpyware to be set in image 2")
	}
	n, err := v.DWORD()
	if err != nil {
		t.Fatalf("DWORD: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected value 1, got %d", n)
	}

	// Image 1 must be untouched: the whole point of generalizing this
	// beyond the debloat datasource's single-image version is that stock
	// media's other editions round-trip unmodified.
	if v1 := readHiveValueFromImage(t, wimPath, 1, registry.HiveSoftware, `Policies\Microsoft\Windows Defender`, "DisableAntiSpyware"); v1 != nil {
		t.Fatalf("expected image 1 to be untouched, but found DisableAntiSpyware set there too")
	}
}

func TestApplyRegistryTweaksToWimImageIndexOutOfRange(t *testing.T) {
	wimPath := buildTestMultiImageWim(t)
	err := applyRegistryTweaksToWim(wimPath, 3, []RegistryTweak{{
		Hive: registry.HiveSoftware, Path: `A\B`, Name: "N", Type: "dword", Value: "1",
	}})
	if err == nil {
		t.Fatalf("expected an error for an out-of-range image_index")
	}
}
