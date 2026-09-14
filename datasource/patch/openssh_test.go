package patch

import (
	"bytes"
	"crypto/sha1"
	"os"
	"strings"
	"testing"

	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/registry"
	"github.com/Pandapip1/gowim/wim"
)

// buildTestSingleImageWimWithControlSet writes a one-image install.wim (an
// empty SOFTWARE hive, and a SYSTEM hive with just enough real structure --
// Select\Default=1 and a ControlSet001 subkey -- for
// service.CurrentControlSet to resolve, exactly like a real, freshly
// captured install.wim's SYSTEM hive already has pre-boot) to a temp file.
func buildTestSingleImageWimWithControlSet(t *testing.T) string {
	t.Helper()

	softwareHive := &regf.Hive{
		BaseBlock: regf.BaseBlock{MajorVersion: 1, MinorVersion: regf.Version1_5, FileType: regf.FileTypePrimary, ClusteringFactor: 1},
		Root:      &regf.Key{Flags: regf.KeyFlagHiveEntry},
	}

	systemRoot := &regf.Key{Flags: regf.KeyFlagHiveEntry}
	selectKey := systemRoot.FindOrCreateSubkey("Select")
	selectKey.SetValue("Default", regf.RegDWORD, regf.EncodeDWORD(1))
	systemRoot.FindOrCreateSubkey("ControlSet001")
	systemHive := &regf.Hive{
		BaseBlock: regf.BaseBlock{MajorVersion: 1, MinorVersion: regf.Version1_5, FileType: regf.FileTypePrimary, ClusteringFactor: 1},
		Root:      systemRoot,
	}

	softwareData, err := softwareHive.AppendTo(nil)
	if err != nil {
		t.Fatalf("SOFTWARE hive AppendTo: %v", err)
	}
	systemData, err := systemHive.AppendTo(nil)
	if err != nil {
		t.Fatalf("SYSTEM hive AppendTo: %v", err)
	}

	bt := &wim.BlobTable{}
	src := wim.MapBlobSource{}
	root := &wim.DirEntry{Attributes: wim.FileAttributeDirectory, SecurityID: wim.SecurityIDNone}
	for path, data := range map[string][]byte{
		`Windows\System32\config\SYSTEM`:   systemData,
		`Windows\System32\config\SOFTWARE`: softwareData,
	} {
		hash := wim.Hash(sha1.Sum(data))
		bt.Entries = append(bt.Entries, wim.BlobDescriptor{Hash: hash, PartNumber: 1, RefCount: 1})
		src[hash] = data
		if _, err := root.Add(path, hash); err != nil {
			t.Fatalf("Add(%s): %v", path, err)
		}
	}
	images := []*wim.ImageMetadata{{Security: &wim.SecurityData{}, Root: root}}

	wimBytes, err := wim.Assemble(images, bt, &wim.XMLData{}, src, wim.WriteOptions{GUID: wim.GUID{1}})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	f, err := os.CreateTemp("", "patch-openssh-test-*.wim")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	path := f.Name()
	if _, err := f.Write(wimBytes); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	return path
}

// TestInstallOpenSSHToWim installs the real, embedded OpenSSH.Server
// fixtures (see openssh.go's opensshFixtures) into a small synthetic
// install.wim and checks both halves this feature bakes offline: the real
// payload files land where component.Install's own test proves they should,
// and every registry_tweaks-equivalent value (DefaultShell, sshd's Start
// type via the "CurrentControlSet\..." path -- exercising regtweaks.go's
// new CurrentControlSet resolution against a real ControlSet001 -- the
// LocalAccountTokenFilterPolicy, and the FirewallRules entry) is present
// with the right encoding.
func TestInstallOpenSSHToWim(t *testing.T) {
	wimPath := buildTestSingleImageWimWithControlSet(t)

	if err := installOpenSSHToWim(wimPath, 1); err != nil {
		t.Fatalf("installOpenSSHToWim: %v", err)
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
	if len(metas) != 1 {
		t.Fatalf("expected 1 image, got %d", len(metas))
	}
	meta, err := r.ImageMetadata(metas[0])
	if err != nil {
		t.Fatalf("ImageMetadata: %v", err)
	}

	// File placement: sshd.exe must be reachable at its System32 projection
	// with real, non-empty content (the same real fixture bytes
	// gowim/component's own test checks byte-for-byte; here just a sanity
	// check that this integration didn't drop or corrupt it).
	entry, err := meta.Root.Lookup(`Windows\System32\OpenSSH\sshd.exe`)
	if err != nil {
		t.Fatalf("Lookup sshd.exe: %v", err)
	}
	sshdData, err := r.ReadFile(meta.Root, bt, `Windows\System32\OpenSSH\sshd.exe`)
	if err != nil {
		t.Fatalf("ReadFile sshd.exe: %v", err)
	}
	if len(sshdData) < 2 || sshdData[0] != 'M' || sshdData[1] != 'Z' {
		t.Fatalf("sshd.exe: missing MZ magic (got %d bytes)", len(sshdData))
	}
	_ = entry

	hs, err := registry.LoadHiveSet(r, meta.Root, bt)
	if err != nil {
		t.Fatalf("LoadHiveSet: %v", err)
	}

	software := hs.Hives[registry.HiveSoftware].Hive.Root
	if v := software.OpenPath("OpenSSH"); v == nil || v.Value("DefaultShell") == nil {
		t.Errorf("expected SOFTWARE\\OpenSSH!DefaultShell to be set")
	} else if got := v.Value("DefaultShell").SZ(); got == "" {
		t.Errorf("DefaultShell is empty")
	}
	if v := software.OpenPath(`Microsoft\Windows\CurrentVersion\Policies\System`); v == nil || v.Value("LocalAccountTokenFilterPolicy") == nil {
		t.Errorf("expected LocalAccountTokenFilterPolicy to be set")
	} else if n, err := v.Value("LocalAccountTokenFilterPolicy").DWORD(); err != nil || n != 1 {
		t.Errorf("LocalAccountTokenFilterPolicy = %d, %v; want 1", n, err)
	}

	system := hs.Hives[registry.HiveSystem].Hive.Root
	ccs := system.Subkey("ControlSet001")
	if ccs == nil {
		t.Fatalf("ControlSet001 missing after install")
	}
	sshdKey := ccs.OpenPath(`Services\sshd`)
	if sshdKey == nil || sshdKey.Value("Start") == nil {
		t.Errorf("expected ControlSet001\\Services\\sshd!Start to be set")
	} else if n, err := sshdKey.Value("Start").DWORD(); err != nil || n != serviceStartAutomatic {
		t.Errorf("sshd Start = %d, %v; want %d", n, err, serviceStartAutomatic)
	}

	fw := ccs.OpenPath(registry.FirewallRulesPath)
	if fw == nil || len(fw.Values) != 1 {
		t.Fatalf("expected exactly one FirewallRules value, got key=%v", fw)
	}
	fwValue := fw.Values[0].SZ()
	for _, want := range []string{"Action=Allow|", "Dir=In|", "Protocol=6|", "LPort=22|"} {
		if !strings.Contains(fwValue, want) {
			t.Errorf("firewall rule value %q missing %q", fwValue, want)
		}
	}
}
