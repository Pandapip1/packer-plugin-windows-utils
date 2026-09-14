package patch

import (
	"os"
	"testing"
)

// TestBuildBakedISORealBoot is not a self-contained boot test (a real qemu
// boot + SSH connection is orchestrated externally, from bash, since it
// needs disk-usage monitoring between steps and a live qemu process this
// package has no business owning) -- it is only the "produce the patched
// ISO" half, isolated into its own gated test so it can be run and timed
// independently of the rest of this package's fast, offline suite.
//
// Skipped unless PATCH_TEST_REAL_BOOT=1, matching this repo's own
// GOWIM_TEST_IMAGE/GOWIM_TEST_NETWORK convention for gating a real,
// disk/network/time-heavy check behind an explicit opt-in so `go test ./...`
// stays fast and fully offline by default.
func TestBuildBakedISORealBoot(t *testing.T) {
	if os.Getenv("PATCH_TEST_REAL_BOOT") != "1" {
		t.Skip("set PATCH_TEST_REAL_BOOT=1 to run (extracts/repacks a real multi-GB Windows ISO)")
	}
	srcISO := os.Getenv("PATCH_TEST_WIN_ISO")
	if srcISO == "" {
		srcISO = "/mnt/extra/isos/Win11_25H2_EnglishInternational_x64_v2.iso"
	}
	if _, err := os.Stat(srcISO); err != nil {
		t.Fatalf("source ISO %s: %v", srcISO, err)
	}
	outPathFile := os.Getenv("PATCH_TEST_OUT_PATH_FILE")
	if outPathFile == "" {
		t.Fatal("PATCH_TEST_OUT_PATH_FILE must name a file to write the resulting patched ISO's path into")
	}

	// image_index 6 == Windows 11 Pro, matching fanuc-rcs.pkr.hcl's own
	// iso_image_index default for this exact media (read as a reference
	// only, not modified/executed).
	//
	// PATCH_TEST_NO_PROMPT_PATCH lets a diagnostic run isolate whether a
	// real-boot failure comes from patchNoBootPromptDir's binary patch
	// (default "1"/true, matching normal windows-utils-patch usage) versus
	// the enable_ssh/registry_tweaks rebuild path itself.
	patchNoBoot := os.Getenv("PATCH_TEST_NO_PROMPT_PATCH") != "0"
	patchedPath, err := patchISO(srcISO, patchNoBoot, nil, nil, 6, true)
	if err != nil {
		t.Fatalf("patchISO: %v", err)
	}
	t.Logf("patched ISO: %s", patchedPath)
	if err := os.WriteFile(outPathFile, []byte(patchedPath), 0o644); err != nil {
		t.Fatalf("writing out path file: %v", err)
	}
}
