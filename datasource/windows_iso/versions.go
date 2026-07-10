package windows_iso

// edition is a selectable Windows edition (e.g. "Home/Pro/Edu") or, for UEFI
// Shell entries, a build variant ("Release"/"Debug"). ids holds one Microsoft
// productEditionId per queryable SKU group — Windows 11 ships x64/x86 SKUs
// under one id and ARM64 SKUs under another, so more than one id may need to
// be queried to cover every architecture of a single edition.
type edition struct {
	name string
	ids  []int
}

type release struct {
	name     string
	editions []edition
}

type winVersion struct {
	name     string
	pageType string
	releases []release
}

// windowsVersions mirrors the retail-Windows entries of the $WindowsVersions
// table in https://github.com/pbatard/Fido/blob/master/Fido.ps1. Microsoft
// only keeps download links for the latest release of each version live, so
// (as in Fido) only one release is listed per version.
var windowsVersions = []winVersion{
	{
		name:     "Windows 11",
		pageType: "windows11",
		releases: []release{
			{
				name: "25H2 v2 (Build 26200.8037 - 2026.03)",
				editions: []edition{
					{name: "Windows 11 Home/Pro/Edu", ids: []int{3321, 3324}},
					{name: "Windows 11 Home China", ids: []int{3322, 3325}},
					{name: "Windows 11 Pro China", ids: []int{3323, 3326}},
				},
			},
		},
	},
	{
		name:     "Windows 10",
		pageType: "Windows10ISO",
		releases: []release{
			{
				name: "22H2 v1 (Build 19045.2965 - 2023.05)",
				editions: []edition{
					{name: "Windows 10 Home/Pro/Edu", ids: []int{2618}},
					{name: "Windows 10 Home China", ids: []int{2378}},
				},
			},
		},
	},
}

// uefiShellEditions is the Release(0)/Debug(1) build-variant pair shared by
// every UEFI Shell 2.2 release entry.
var uefiShellEditions = []edition{
	{name: "Release", ids: []int{0}},
	{name: "Debug", ids: []int{1}},
}

// uefiShellVersions mirrors the UEFI Shell entries of the same table. Their
// download link is a static GitHub release asset rather than a Microsoft
// download-connector lookup.
var uefiShellVersions = []winVersion{
	{
		name:     "UEFI Shell 2.2",
		pageType: "UEFI_SHELL 2.2",
		releases: []release{
			{name: "26H1 (edk2-stable202602)", editions: uefiShellEditions},
			{name: "25H2 (edk2-stable202511)", editions: uefiShellEditions},
			{name: "25H1 (edk2-stable202505)", editions: uefiShellEditions},
			{name: "24H2 (edk2-stable202411)", editions: uefiShellEditions},
			{name: "24H1 (edk2-stable202405)", editions: uefiShellEditions},
			{name: "23H2 (edk2-stable202311)", editions: uefiShellEditions},
			{name: "23H1 (edk2-stable202305)", editions: uefiShellEditions},
			{name: "22H2 (edk2-stable202211)", editions: uefiShellEditions},
			{name: "22H1 (edk2-stable202205)", editions: uefiShellEditions},
			{name: "21H2 (edk2-stable202108)", editions: uefiShellEditions},
			{name: "21H1 (edk2-stable202105)", editions: uefiShellEditions},
			{name: "20H2 (edk2-stable202011)", editions: uefiShellEditions},
		},
	},
	{
		name:     "UEFI Shell 2.0",
		pageType: "UEFI_SHELL 2.0",
		releases: []release{
			{name: "4.632 [20100426]", editions: []edition{{name: "Release", ids: []int{0}}}},
		},
	},
}
