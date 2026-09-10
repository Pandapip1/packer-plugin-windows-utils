package debloat

import (
	"crypto/rand"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Pandapip1/gowim/lzx"
	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/registry"
	"github.com/Pandapip1/gowim/wim"
)

// knownHives is the set of RegistryTweak.Hive values accepted in HCL,
// matching gowim/registry's own Hive* constants 1:1 (a tweak's Hive string
// is used directly as the HiveSet map key -- see applyOneTweak).
var knownHives = map[string]bool{
	registry.HiveSystem:      true,
	registry.HiveSoftware:    true,
	registry.HiveDefault:     true,
	registry.HiveSAM:         true,
	registry.HiveComponents:  true,
	registry.HiveDefaultUser: true,
}

func isKnownHive(name string) bool {
	return knownHives[name]
}

// applyRegistryTweaksToWim opens the single-image install.wim at wimPath
// (as produced by nano11-go's own debloat pass -- see run() in nano11-go's
// main.go, which always writes a "debloated single-image install.wim"),
// loads its standard registry hive set via gowim/registry, applies every
// tweak, and rewrites wimPath in place with the modified hives.
//
// This is exactly how you'd pre-bake e.g.
// SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System!
// LocalAccountTokenFilterPolicy=1 (dword) into the image itself, instead of
// doing it at runtime via an Autounattend.xml specialize-pass
// RunSynchronousCommand invoking reg.exe -- the edit happens once, offline,
// before the image is ever booted, and is baked into every install from
// this ISO.
//
// The read/modify/rebuild-blob-table/write sequence below mirrors nano11-go
// main.go's own run() function (which does the exact same thing for its own
// built-in registry tweaks), simplified because this is always a
// single-image WIM (nano11-go's own -out is single-image already) rather
// than a multi-edition source WIM needing an export pass first.
func applyRegistryTweaksToWim(wimPath string, tweaks []RegistryTweak) error {
	f, err := os.Open(wimPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", wimPath, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}

	r, err := wim.NewReader(f, fi.Size())
	if err != nil {
		return fmt.Errorf("read %s: %w", wimPath, err)
	}
	bt, err := r.BlobTable()
	if err != nil {
		return err
	}
	xmlData, err := r.XMLData()
	if err != nil {
		return err
	}
	metaResources := bt.MetadataResources()
	if len(metaResources) != 1 {
		return fmt.Errorf("expected exactly 1 image in %s, got %d", wimPath, len(metaResources))
	}
	meta, err := r.ImageMetadata(metaResources[0])
	if err != nil {
		return err
	}
	root := meta.Root

	hs, err := registry.LoadHiveSet(r, root, bt)
	if err != nil {
		return fmt.Errorf("load hive set: %w", err)
	}

	for _, t := range tweaks {
		if err := applyOneTweak(hs, t); err != nil {
			return err
		}
	}

	newBlobs := map[wim.Hash][]byte{}
	for name, h := range hs.Hives {
		nb, err := h.Save(bt)
		if err != nil {
			return fmt.Errorf("save %s hive: %w", name, err)
		}
		if nb.Data != nil {
			newBlobs[nb.Hash] = nb.Data
		}
	}

	rebuiltBT, err := wim.RebuildBlobTable([]*wim.ImageMetadata{meta}, bt)
	if err != nil {
		return fmt.Errorf("rebuild blob table: %w", err)
	}

	outPath := wimPath + ".tweaked.tmp"
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}

	src := combinedBlobSource{overrides: newBlobs, fallback: wim.NewReaderBlobSource(r, bt)}
	_, err = wim.WriteTo(out, []*wim.ImageMetadata{meta}, rebuiltBT, xmlData, src, wim.WriteOptions{
		CompressionType: wim.HdrFlagCompressLZX,
		ChunkSize:       32768,
		BootIndex:       1,
		GUID:            randomGUID(),
		LZXOptions:      lzx.Fast(),
	})
	closeErr := out.Close()
	if err != nil {
		os.Remove(outPath)
		return fmt.Errorf("write tweaked %s: %w", wimPath, err)
	}
	if closeErr != nil {
		os.Remove(outPath)
		return closeErr
	}

	// f (the original wimPath) must be closed before it can be replaced.
	f.Close()
	if err := os.Rename(outPath, wimPath); err != nil {
		return fmt.Errorf("replace %s with tweaked copy: %w", wimPath, err)
	}
	return nil
}

func applyOneTweak(hs *registry.HiveSet, t RegistryTweak) error {
	h, ok := hs.Hives[t.Hive]
	if !ok {
		return fmt.Errorf("registry_tweaks: hive %s not present in this image", t.Hive)
	}
	key := h.Hive.Root.FindOrCreatePath(strings.TrimPrefix(t.Path, `\`))

	switch t.Type {
	case "dword":
		n, err := strconv.ParseUint(t.Value, 0, 32)
		if err != nil {
			return fmt.Errorf("registry_tweaks: %s\\%s!%s: invalid dword %q: %w", t.Hive, t.Path, t.Name, t.Value, err)
		}
		key.SetValue(t.Name, regf.RegDWORD, regf.EncodeDWORD(uint32(n)))
	case "string":
		key.SetValue(t.Name, regf.RegSZ, regf.EncodeSZ(t.Value))
	default:
		return fmt.Errorf("registry_tweaks: %s\\%s!%s: unknown type %q", t.Hive, t.Path, t.Name, t.Type)
	}
	return nil
}

// combinedBlobSource serves a blob from overrides (newly written hive
// content) first, falling back to the source WIM otherwise. Mirrors
// nano11-go's own treeutil.go type of the same name/shape.
type combinedBlobSource struct {
	overrides map[wim.Hash][]byte
	fallback  wim.BlobSource
}

func (c combinedBlobSource) Blob(h wim.Hash) ([]byte, error) {
	if data, ok := c.overrides[h]; ok {
		return data, nil
	}
	return c.fallback.Blob(h)
}

// randomGUID generates a real random GUID for wim.WriteOptions.GUID, which
// must be explicitly set to a nonzero value (WriteTo/Assemble do not
// generate one themselves). Mirrors nano11-go's own filecleanup.go helper
// of the same name.
func randomGUID() wim.GUID {
	var g wim.GUID
	if _, err := rand.Read(g[:]); err != nil {
		panic(fmt.Sprintf("randomGUID: %v", err))
	}
	return g
}
