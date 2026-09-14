package patch

import (
	"crypto/rand"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/registry"
	"github.com/Pandapip1/gowim/service"
	"github.com/Pandapip1/gowim/wim"
)

// RegistryTweak mirrors debloat.RegistryTweak's shape (kept as its own type
// since patch and debloat are independent packages, neither importing the
// other) -- see debloat.RegistryTweak's doc comment for the general
// offline-tweak rationale (baking a value into the image itself rather than
// setting it at runtime via an Autounattend.xml specialize-pass command).
type RegistryTweak struct {
	// Hive is one of "SYSTEM", "SOFTWARE", "DEFAULT", "SAM", "COMPONENTS", or
	// "NTUSER.DAT" (Users\Default\NTUSER.DAT), matching gowim/registry's
	// Hive* constants.
	Hive string
	// Path is the key path within Hive, backslash-separated and without a
	// leading backslash.
	Path string
	// Name is the value name within Path.
	Name string
	// Type is "dword" or "string" (case-insensitive).
	Type string
	// Value is the value data: a base-10 (or "0x"-prefixed base-16) integer
	// literal for Type "dword", or a literal string for Type "string".
	Value string
}

var knownHives = map[string]bool{
	registry.HiveSystem:      true,
	registry.HiveSoftware:    true,
	registry.HiveDefault:     true,
	registry.HiveSAM:         true,
	registry.HiveComponents:  true,
	registry.HiveDefaultUser: true,
}

// IsKnownHive reports whether name is one of gowim/registry's own Hive*
// constants, exported for the datasource's own HCL validation.
func IsKnownHive(name string) bool { return knownHives[name] }

// applyRegistryTweaksToWim applies tweaks against the imageIndex'th (1-based,
// matching Windows WIM image indices and this plugin's own image_index HCL
// field) image inside the multi-edition install.wim at wimPath, preserving
// every other image in the file untouched, and rewrites wimPath in place.
//
// This mirrors the debloat datasource's own regtweaks.go
// applyRegistryTweaksToWim, generalized for a multi-image source WIM: stock
// Windows installation media ships every edition in one install.wim (unlike
// nano11-go's own single-image output, which is why the debloat datasource's
// version could assume exactly one image), so this one locates and edits
// only the caller-selected image while still passing every image through to
// RebuildBlobTable/WriteTo so the rest of the editions round-trip byte-for-
// byte (modulo the tweaked image's own hive blobs).
func applyRegistryTweaksToWim(wimPath string, imageIndex int, tweaks []RegistryTweak) error {
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
	if imageIndex < 1 || imageIndex > len(metaResources) {
		return fmt.Errorf("registry_tweaks: image_index %d out of range (wim has %d image(s))", imageIndex, len(metaResources))
	}

	images := make([]*wim.ImageMetadata, len(metaResources))
	for i, mr := range metaResources {
		meta, err := r.ImageMetadata(mr)
		if err != nil {
			return fmt.Errorf("read image %d metadata: %w", i+1, err)
		}
		images[i] = meta
	}
	target := images[imageIndex-1]

	hs, err := registry.LoadHiveSet(r, target.Root, bt)
	if err != nil {
		return fmt.Errorf("load hive set: %w", err)
	}

	for _, t := range tweaks {
		if err := applyOneRegistryTweak(hs, t); err != nil {
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

	rebuiltBT, err := wim.RebuildBlobTable(images, bt)
	if err != nil {
		return fmt.Errorf("rebuild blob table: %w", err)
	}

	outPath := wimPath + ".tweaked.tmp"
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}

	// Uncompressed, not LZX: this ISO is an ephemeral, single-use Packer
	// build artifact (patched once, booted once, then discarded), so
	// there's nothing to gain from spending CPU time compressing it - and
	// a lot to lose: LZX-recompressing a multi-GB install.wim was
	// confirmed on a live builder to take well over two hours of CPU
	// time, versus a few seconds uncompressed.
	src := regTweakBlobSource{overrides: newBlobs, fallback: wim.NewReaderBlobSource(r, bt)}
	_, err = wim.WriteTo(out, images, rebuiltBT, xmlData, src, wim.WriteOptions{
		CompressionType: wim.CompressionNone,
		BootIndex:       1,
		GUID:            randomRegTweakGUID(),
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

func applyOneRegistryTweak(hs *registry.HiveSet, t RegistryTweak) error {
	h, ok := hs.Hives[t.Hive]
	if !ok {
		return fmt.Errorf("registry_tweaks: hive %s not present in this image", t.Hive)
	}

	path := strings.TrimPrefix(t.Path, `\`)
	base := h.Hive.Root

	// A path rooted at "CurrentControlSet" (e.g. what a live
	// Set-Service/New-NetFirewallRule targets, and what enable-ssh.ps1's own
	// runtime equivalents write under) has no on-disk key of that name in an
	// offline SYSTEM hive -- it's a symbolic-link concept the running kernel
	// resolves, not a persistent subtree (see service.CurrentControlSet's
	// doc comment). Resolve it the same way that package does (via
	// SYSTEM\Select\Default) before falling through to FindOrCreatePath, so
	// registry_tweaks callers can write "CurrentControlSet\..." the same way
	// they'd type it in regedit/PowerShell rather than having to know the
	// real ControlSet00N number themselves.
	const ccsPrefix = `CurrentControlSet\`
	if t.Hive == registry.HiveSystem && (path == "CurrentControlSet" || strings.HasPrefix(path, ccsPrefix)) {
		ccs, err := service.CurrentControlSet(h.Hive.Root)
		if err != nil {
			return fmt.Errorf("registry_tweaks: %s\\%s!%s: resolving CurrentControlSet: %w", t.Hive, t.Path, t.Name, err)
		}
		base = ccs
		path = strings.TrimPrefix(path, "CurrentControlSet")
		path = strings.TrimPrefix(path, `\`)
	}

	var key *regf.Key
	if path == "" {
		key = base
	} else {
		key = base.FindOrCreatePath(path)
	}

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

// regTweakBlobSource serves a blob from overrides (newly written hive
// content) first, falling back to the source WIM otherwise.
type regTweakBlobSource struct {
	overrides map[wim.Hash][]byte
	fallback  wim.BlobSource
}

func (c regTweakBlobSource) Blob(h wim.Hash) ([]byte, error) {
	if data, ok := c.overrides[h]; ok {
		return data, nil
	}
	return c.fallback.Blob(h)
}

// randomRegTweakGUID generates a real random GUID for wim.WriteOptions.GUID,
// which must be explicitly set to a nonzero value (WriteTo/Assemble do not
// generate one themselves).
func randomRegTweakGUID() wim.GUID {
	var g wim.GUID
	if _, err := rand.Read(g[:]); err != nil {
		panic(fmt.Sprintf("randomRegTweakGUID: %v", err))
	}
	return g
}
