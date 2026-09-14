package patch

import (
	"embed"
	"fmt"
	"log"
	"os"

	"github.com/Pandapip1/gowim/component"
	"github.com/Pandapip1/gowim/registry"
	"github.com/Pandapip1/gowim/wim"
)

// opensshFixtures embeds the real, genuine OpenSSH.Server Windows Capability
// payload for Windows 11 25H2 (build 26200), amd64 -- the same real files
// gowim's own component/install_openssh_test.go proves component.Install
// places correctly, byte-for-byte. See that test's doc comment for full
// provenance: downloaded live from Microsoft's own update CDN by gowim's
// wufetch package, extracted by gowim's cab package (itself verified against
// cabextract), and PA30-decoded to plain XML via gowim's pa30 package.
// Copied here verbatim rather than re-derived at build time.
//
// This hardcodes one build/architecture. A general "fetch the right
// capability for whatever ISO the caller handed us" pipeline would need to
// (a) determine the target image's exact build number (available via this
// image's own SOFTWARE\Microsoft\Windows NT\CurrentVersion\CurrentBuild, or
// the install.wim's XML metadata) and its architecture, (b) call
// wufetch.Client.DownloadCapability for that build/arch live, (c) extract the
// result via the cab package, and (d) PA30-decode its manifests -- which
// needs wcp.dll's shared PA30 dictionary (see gowim/pa30/README.md's
// "extracted via standard, documented PE-resource extraction" note). That
// last step has no public, redistributable source in gowim today (the
// dictionary lives only in pa30's own testdata, used for its tests) --
// generalizing beyond this one hardcoded build is left as follow-up work,
// not attempted here since only one concrete target (Windows 11 25H2/26200/
// amd64, matching the actual ISO this feature was validated against) is
// needed right now.
//
//go:embed testdata/openssh
var opensshFixtures embed.FS

const (
	// These KeyForms are synthetic-but-well-formed (16 hex digits, matching
	// a real WinSxS keyform's shape) -- not the real CBS identity hash,
	// which gowim cannot compute (see component.ComponentInstall.KeyForm's
	// doc comment). Fine for BuildOnce: no COMPONENTS hive entry is written
	// that would need to agree with a real keyform.
	opensshServerKeyForm = "amd64_openssh-server-components-onecore_deadbeefcafef00d"
	opensshCommonKeyForm = "amd64_openssh-common-components-onecore_cafebabedeadbeef"

	// sshdServiceName is the service gowim/service's own install machinery
	// would use if this were creating the service from scratch; here the
	// service key already exists (OpenSSH-Server-Package's own MUM/manifest
	// -- specifically the payload's own service-install metadata baked into
	// Windows' catalog -- is what actually creates
	// SYSTEM\CurrentControlSet\Services\sshd on a real Add-WindowsCapability
	// install). This package only flips its Start type, exactly like
	// `Set-Service -Name sshd -StartupType Automatic` does at runtime (see
	// enable-ssh.ps1.pkrtpl.hcl).
	sshdServiceName = "sshd"

	// serviceStartAutomatic is the well-documented SERVICE_AUTO_START value
	// (0x2) for a service's Start REG_DWORD -- see Microsoft's "Services"
	// registry tree documentation
	// (https://learn.microsoft.com/en-us/windows/win32/services/service-records-in-the-registry:
	// "0x2 -- SERVICE_AUTO_START ... started by the SCM at system startup").
	serviceStartAutomatic = 2
)

func readOpenSSHFixture(path string) ([]byte, error) {
	data, err := opensshFixtures.ReadFile("testdata/openssh/" + path)
	if err != nil {
		return nil, fmt.Errorf("openssh: reading embedded fixture %s: %w", path, err)
	}
	return data, nil
}

// opensshInstallation builds the component.Installation this package installs
// into an image's install.wim: OpenSSH.Server's two real components
// (OpenSSH-Server-Components-Onecore and OpenSSH-Common-Components-Onecore)
// plus their package MUM, exactly mirroring gowim's own
// TestInstall_RealOpenSSHCapability. Serviceability is BuildOnce: this
// produces a throwaway, never-serviced deploy image (matching
// component/README.md's own build-once-vs-serviceable tradeoff table) --
// InstallRegistry is deliberately not called.
func opensshInstallation() (*component.Installation, error) {
	files := map[string]*[]byte{}
	load := func(dst *[]byte, path string) {
		files[path] = dst
	}
	var serverManifest, commonManifest, sshdExe, moduli, sftpServer, sshShellhost, sshdConfig []byte
	var sshKeygen, sshAgent, scp, sshAdd, packageMUM []byte
	load(&serverManifest, "server_component/component.manifest")
	load(&commonManifest, "common_component/component.manifest")
	load(&sshdExe, "server_component/sshd.exe")
	load(&moduli, "server_component/moduli")
	load(&sftpServer, "server_component/sftp-server.exe")
	load(&sshShellhost, "server_component/ssh-shellhost.exe")
	load(&sshdConfig, "server_component/sshd_config_default")
	load(&sshKeygen, "common_component/ssh-keygen.exe")
	load(&sshAgent, "common_component/ssh-agent.exe")
	load(&scp, "common_component/scp.exe")
	load(&sshAdd, "common_component/ssh-add.exe")
	load(&packageMUM, "OpenSSH-Server-Package.mum")

	for path, dst := range files {
		data, err := readOpenSSHFixture(path)
		if err != nil {
			return nil, err
		}
		*dst = data
	}

	return &component.Installation{
		Serviceability: component.BuildOnce,
		Components: []component.ComponentInstall{
			{
				KeyForm:  opensshServerKeyForm,
				Manifest: serverManifest,
				Files: []component.PayloadFile{
					{Name: "sshd.exe", Data: sshdExe, DestDirs: []string{`Windows\System32\OpenSSH`}},
					{Name: "moduli", Data: moduli, DestDirs: []string{`Windows\System32\OpenSSH`}},
					{Name: "sftp-server.exe", Data: sftpServer, DestDirs: []string{`Windows\System32\OpenSSH`}},
					{Name: "ssh-shellhost.exe", Data: sshShellhost, DestDirs: []string{`Windows\System32\OpenSSH`}},
					{Name: "sshd_config_default", Data: sshdConfig, DestDirs: []string{`Windows\System32\OpenSSH`}},
				},
			},
			{
				KeyForm:  opensshCommonKeyForm,
				Manifest: commonManifest,
				Files: []component.PayloadFile{
					{Name: "ssh-keygen.exe", Data: sshKeygen, DestDirs: []string{`Windows\System32\OpenSSH`}},
					{Name: "ssh-agent.exe", Data: sshAgent, DestDirs: []string{`Windows\System32\OpenSSH`}},
					{Name: "scp.exe", Data: scp, DestDirs: []string{`Windows\System32\OpenSSH`}},
					{Name: "ssh-add.exe", Data: sshAdd, DestDirs: []string{`Windows\System32\OpenSSH`}},
				},
			},
		},
		Packages: []component.PackageInstall{
			{
				Name: "OpenSSH-Server-Package~31bf3856ad364e35~amd64~~10.0.26100.1",
				MUM:  packageMUM,
			},
		},
	}, nil
}

// opensshRegistryTweaks returns the registry_tweaks equivalent of every
// setting enable-ssh.ps1.pkrtpl.hcl applies at runtime (read as a spec, not
// modified) other than Add-WindowsCapability/Start-Service themselves (which
// this offline path replaces with component.Install placing the files
// directly, and the service's Start type below, respectively):
//
//   - HKLM\SOFTWARE\OpenSSH!DefaultShell = the path to powershell.exe.
//   - SYSTEM's sshd service Start type = SERVICE_AUTO_START (what
//     `Set-Service -StartupType Automatic` writes), so sshd starts on first
//     boot with no specialize-pass script needed at all.
//   - HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System!
//     LocalAccountTokenFilterPolicy = 1, exactly as enable-ssh.ps1.pkrtpl.hcl
//     comments explain (a local administrator connecting over the network
//     otherwise gets a UAC-filtered token).
//   - The inbound TCP/22 allow firewall rule enable-ssh.ps1.pkrtpl.hcl
//     creates via New-NetFirewallRule, authored directly into
//     FirewallRules via gowim/registry's new EncodeFirewallRuleValue (see
//     that package's firewall.go).
//
// powershellPath is the path enable-ssh.ps1.pkrtpl.hcl resolves at runtime
// via `(Get-Command powershell.exe).Source`; since this runs offline against
// an image nothing is executing in, the well-known, version-independent
// path is used instead (confirmed to be the actual real path on every
// current Windows release: %SystemRoot%\System32\WindowsPowerShell\v1.0\
// powershell.exe -- Microsoft's own PowerShell install documentation and
// every real Windows install's own directory layout agree this path has
// never changed since PowerShell 1.0, "v1.0" naming the directory rather
// than the shipped engine version).
func opensshRegistryTweaks() ([]RegistryTweak, error) {
	const powershellPath = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`

	firewallValue, err := registry.EncodeFirewallRuleValue(registry.FirewallRule{
		Name:        "Packer SSH",
		Description: "Allow inbound SSH (port 22), baked offline by packer-plugin-windows-utils",
		Direction:   "In",
		Protocol:    6,
		LocalPort:   22,
		Action:      "Allow",
	})
	if err != nil {
		return nil, fmt.Errorf("openssh: encoding firewall rule: %w", err)
	}
	firewallValueName, err := registry.NewFirewallRuleValueName()
	if err != nil {
		return nil, fmt.Errorf("openssh: generating firewall rule value name: %w", err)
	}

	return []RegistryTweak{
		{
			Hive: registry.HiveSoftware, Path: `OpenSSH`, Name: "DefaultShell",
			Type: "string", Value: powershellPath,
		},
		{
			Hive: registry.HiveSystem, Path: `CurrentControlSet\Services\` + sshdServiceName, Name: "Start",
			Type: "dword", Value: fmt.Sprintf("%d", serviceStartAutomatic),
		},
		{
			Hive: registry.HiveSoftware, Path: `Microsoft\Windows\CurrentVersion\Policies\System`, Name: "LocalAccountTokenFilterPolicy",
			Type: "dword", Value: "1",
		},
		{
			Hive: registry.HiveSystem, Path: `CurrentControlSet\` + registry.FirewallRulesPath, Name: firewallValueName,
			Type: "string", Value: firewallValue,
		},
	}, nil
}

// installOpenSSHToWim installs the embedded OpenSSH.Server capability (see
// opensshInstallation) into the imageIndex'th image inside the multi-edition
// install.wim at wimPath, then applies opensshRegistryTweaks against the same
// image -- all in one load/mutate/save pass, so the file-placement and
// registry-tweak halves round-trip together against one rewritten WIM rather
// than two. Structured as a sibling to applyRegistryTweaksToWim (same
// image-selection, hive-loading, and blob-table-rebuild shape), extended with
// the component.Install step first.
func installOpenSSHToWim(wimPath string, imageIndex int) error {
	f, err := os.Open(wimPath)
	if err != nil {
		return fmt.Errorf("openssh: open %s: %w", wimPath, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}

	r, err := wim.NewReader(f, fi.Size())
	if err != nil {
		return fmt.Errorf("openssh: read %s: %w", wimPath, err)
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
		return fmt.Errorf("openssh: image_index %d out of range (wim has %d image(s))", imageIndex, len(metaResources))
	}

	images := make([]*wim.ImageMetadata, len(metaResources))
	for i, mr := range metaResources {
		meta, err := r.ImageMetadata(mr)
		if err != nil {
			return fmt.Errorf("openssh: read image %d metadata: %w", i+1, err)
		}
		images[i] = meta
	}
	target := images[imageIndex-1]

	inst, err := opensshInstallation()
	if err != nil {
		return err
	}

	newRoot, newFileBlobs, err := component.Install(target, bt, inst)
	if err != nil {
		return fmt.Errorf("openssh: component.Install: %w", err)
	}
	target.Root = newRoot

	tweaks, err := opensshRegistryTweaks()
	if err != nil {
		return err
	}

	hs, err := registry.LoadHiveSet(r, target.Root, bt)
	if err != nil {
		return fmt.Errorf("openssh: load hive set: %w", err)
	}
	for _, t := range tweaks {
		if err := applyOneRegistryTweak(hs, t); err != nil {
			return err
		}
	}

	newHiveBlobs := map[wim.Hash][]byte{}
	for name, h := range hs.Hives {
		nb, err := h.Save(bt)
		if err != nil {
			return fmt.Errorf("openssh: save %s hive: %w", name, err)
		}
		if nb.Data != nil {
			newHiveBlobs[nb.Hash] = nb.Data
		}
	}

	overrides := make(map[wim.Hash][]byte, len(newHiveBlobs)+len(newFileBlobs))
	for h, d := range newHiveBlobs {
		overrides[h] = d
	}
	for _, b := range newFileBlobs {
		overrides[b.Hash] = b.Data
	}

	rebuiltBT, err := wim.RebuildBlobTable(images, bt)
	if err != nil {
		return fmt.Errorf("openssh: rebuild blob table: %w", err)
	}

	outPath := wimPath + ".openssh.tmp"
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}

	src := regTweakBlobSource{overrides: overrides, fallback: wim.NewReaderBlobSource(r, bt)}
	_, err = wim.WriteTo(out, images, rebuiltBT, xmlData, src, wim.WriteOptions{
		CompressionType: wim.CompressionNone,
		BootIndex:       1,
		GUID:            randomRegTweakGUID(),
	})
	closeErr := out.Close()
	if err != nil {
		os.Remove(outPath)
		return fmt.Errorf("openssh: write patched %s: %w", wimPath, err)
	}
	if closeErr != nil {
		os.Remove(outPath)
		return closeErr
	}

	f.Close()
	if err := os.Rename(outPath, wimPath); err != nil {
		return fmt.Errorf("openssh: replace %s with patched copy: %w", wimPath, err)
	}

	log.Printf("  Installed OpenSSH.Server (%d components, %d new file blob(s)) and %d registry tweak(s) into install.wim image %d.",
		len(inst.Components), len(newFileBlobs), len(tweaks), imageIndex)
	return nil
}
