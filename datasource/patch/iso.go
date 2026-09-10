package patch

import (
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

const scanStep = 2048

type fileExtent struct {
	lba  uint32
	size uint64
}

// eltoritoEFILBA returns the LBA of the first EFI section entry from the El Torito boot catalog.
func eltoritoEFILBA(path string) (uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var catLBABuf [4]byte
	if _, err := f.ReadAt(catLBABuf[:], 17*scanStep+71); err != nil {
		return 0, fmt.Errorf("reading El Torito catalog LBA: %w", err)
	}
	catLBA := binary.LittleEndian.Uint32(catLBABuf[:])

	cat := make([]byte, 2048)
	if _, err := f.ReadAt(cat, int64(catLBA)*scanStep); err != nil {
		return 0, fmt.Errorf("reading El Torito catalog: %w", err)
	}

	pos := 64
	for pos+32 <= 512 {
		hid := cat[pos]
		if hid == 0x90 || hid == 0x91 {
			count := int(binary.LittleEndian.Uint16(cat[pos+2:]))
			pos += 32
			if count > 0 && pos+32 <= 512 {
				return binary.LittleEndian.Uint32(cat[pos+8:]), nil
			}
		} else {
			break
		}
	}
	return 0, fmt.Errorf("no EFI El Torito entry found")
}

// fatImageSize returns the total byte size of a FAT12/16/32 image, or 0 if the
// data does not look like a FAT volume.
func fatImageSize(data []byte) int {
	if len(data) < 36 {
		return 0
	}
	if data[0] != 0xEB {
		return 0
	}
	if string(data[3:11]) != "MSDOS5.0" {
		return 0
	}
	bps := int(binary.LittleEndian.Uint16(data[11:]))
	tot := int(binary.LittleEndian.Uint16(data[19:]))
	if tot == 0 {
		tot = int(binary.LittleEndian.Uint32(data[32:]))
	}
	return tot * bps
}

// fatReadDir returns the raw 32-byte entries for up to count non-deleted,
// non-LFN slots starting at byte offset off inside data.
func fatReadDir(data []byte, off, count, sz int) [][]byte {
	var entries [][]byte
	for i := 0; i < count; i++ {
		pos := off + i*32
		if pos+32 > sz {
			break
		}
		e := data[pos : pos+32]
		if e[0] == 0 || e[0] == 0xE5 || e[11] == 0x0F {
			continue
		}
		cp := make([]byte, 32)
		copy(cp, e)
		entries = append(entries, cp)
	}
	return entries
}

// fatBootx64Hash walks EFI/BOOT/BOOTX64.EFI inside a FAT12 image and returns
// the hex MD5 of its contents, or "" if not found or on any parse error.
func fatBootx64Hash(data []byte) (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = ""
		}
	}()

	if len(data) < 64 {
		return ""
	}
	bps := int(binary.LittleEndian.Uint16(data[11:]))
	if bps == 0 {
		return ""
	}
	spc := int(data[13])
	res := int(binary.LittleEndian.Uint16(data[14:]))
	nfats := int(data[16])
	rdent := int(binary.LittleEndian.Uint16(data[17:]))
	spf := int(binary.LittleEndian.Uint16(data[22:]))
	rootStart := (res + nfats*spf) * bps
	dataStart := rootStart + rdent*32
	sz := len(data)

	for _, e := range fatReadDir(data, rootStart, rdent, sz) {
		if strings.TrimRight(string(e[0:8]), " ") != "EFI" || e[11]&0x10 == 0 {
			continue
		}
		clus := int(binary.LittleEndian.Uint16(e[26:]))
		efiOff := dataStart + (clus-2)*spc*bps
		for _, e2 := range fatReadDir(data, efiOff, 64, sz) {
			if string(e2[0:4]) != "BOOT" || e2[11]&0x10 == 0 {
				continue
			}
			clus2 := int(binary.LittleEndian.Uint16(e2[26:]))
			bootOff := dataStart + (clus2-2)*spc*bps
			for _, e3 := range fatReadDir(data, bootOff, 64, sz) {
				name := strings.TrimRight(string(e3[0:8]), " ") + "." + strings.TrimRight(string(e3[8:11]), " ")
				if !strings.Contains(name, "BOOTX64") {
					continue
				}
				clus3 := int(binary.LittleEndian.Uint16(e3[26:]))
				fsize := int(binary.LittleEndian.Uint32(e3[28:]))
				foff := dataStart + (clus3-2)*spc*bps
				if foff+fsize <= sz {
					h := md5.Sum(data[foff : foff+fsize])
					return fmt.Sprintf("%x", h)
				}
			}
		}
	}
	return ""
}

func utf16BEDecode(raw []byte) string {
	if len(raw)%2 != 0 {
		raw = raw[:len(raw)-1]
	}
	u16 := make([]uint16, len(raw)/2)
	for i := range u16 {
		u16[i] = binary.BigEndian.Uint16(raw[2*i:])
	}
	return string(utf16.Decode(u16))
}

// udfState holds shared state for UDF traversal.
type udfState struct {
	f              *os.File
	partitionStart uint32
}

func (s *udfState) feLayout(fe []byte) (icbFlags int, infoLen uint64, lAD int, adStart int) {
	icbFlags = int(binary.LittleEndian.Uint16(fe[34:])) & 7
	infoLen = binary.LittleEndian.Uint64(fe[56:])
	lEA := int(binary.LittleEndian.Uint32(fe[168:]))
	lAD = int(binary.LittleEndian.Uint32(fe[172:]))
	adStart = 176 + lEA
	return
}

func (s *udfState) readDirData(fe []byte) []byte {
	icbFlags, _, lAD, adStart := s.feLayout(fe)
	var data []byte
	switch icbFlags {
	case 3:
		data = make([]byte, lAD)
		copy(data, fe[adStart:adStart+lAD])
	case 0, 1:
		step := 8
		if icbFlags == 1 {
			step = 16
		}
		for pos := adStart; pos+step <= adStart+lAD; pos += step {
			elen := int(binary.LittleEndian.Uint32(fe[pos:]))
			if (elen >> 30) != 1 {
				elen &= 0x3FFFFFFF
				elbn := binary.LittleEndian.Uint32(fe[pos+4:])
				if elen > 0 {
					buf := make([]byte, elen)
					s.f.ReadAt(buf, int64(s.partitionStart+elbn)*scanStep) //nolint:errcheck
					data = append(data, buf...)
				}
			}
		}
	}
	return data
}

func (s *udfState) fileExtent(fe []byte) *fileExtent {
	icbFlags, infoLen, lAD, adStart := s.feLayout(fe)
	step := 8
	if icbFlags == 1 {
		step = 16
	}
	if (icbFlags == 0 || icbFlags == 1) && lAD >= step {
		elbn := binary.LittleEndian.Uint32(fe[adStart+4:])
		return &fileExtent{lba: s.partitionStart + elbn, size: infoLen}
	}
	return nil
}

func (s *udfState) walk(lbn uint32, parts []string) *fileExtent {
	fe := make([]byte, scanStep)
	if _, err := s.f.ReadAt(fe, int64(s.partitionStart+lbn)*scanStep); err != nil {
		return nil
	}
	tag := binary.LittleEndian.Uint16(fe[0:])
	if tag != 260 && tag != 261 {
		return nil
	}
	dirData := s.readDirData(fe)
	target := parts[0]
	pos := 0
	for pos+40 <= len(dirData) {
		if binary.LittleEndian.Uint16(dirData[pos:]) != 257 {
			break
		}
		fileChars := dirData[pos+18]
		lFI := int(dirData[pos+19])
		icbLBN := binary.LittleEndian.Uint32(dirData[pos+24:])
		lIU := int(binary.LittleEndian.Uint16(dirData[pos+36:]))
		fiStart := pos + 38 + lIU
		var name string
		if lFI > 0 && fiStart+lFI <= len(dirData) {
			comp := dirData[fiStart]
			raw := dirData[fiStart+1 : fiStart+lFI]
			if comp == 16 {
				name = strings.ToUpper(utf16BEDecode(raw))
			} else {
				name = strings.ToUpper(string(raw))
			}
		}
		if name == target {
			if len(parts) == 1 {
				fe2 := make([]byte, scanStep)
				if _, err := s.f.ReadAt(fe2, int64(s.partitionStart+icbLBN)*scanStep); err != nil {
					return nil
				}
				return s.fileExtent(fe2)
			}
			if fileChars&0x02 != 0 {
				return s.walk(icbLBN, parts[1:])
			}
		}
		fidLen := (38 + lIU + lFI + 3) &^ 3
		if fidLen == 0 {
			break
		}
		pos += fidLen
	}
	return nil
}

// udfFindFile returns the extent for path in the UDF filesystem, or nil.
func udfFindFile(f *os.File, path string) *fileExtent {
	avdp := make([]byte, scanStep)
	if _, err := f.ReadAt(avdp, 256*scanStep); err != nil {
		return nil
	}
	if binary.LittleEndian.Uint16(avdp[0:]) != 2 {
		return nil
	}
	mainVDSLBA := binary.LittleEndian.Uint32(avdp[20:])

	var partitionStart, fsdLBN *uint32
	lba := mainVDSLBA
	for i := 0; i < 32; i++ {
		vd := make([]byte, scanStep)
		if _, err := f.ReadAt(vd, int64(lba)*scanStep); err != nil {
			break
		}
		tag := binary.LittleEndian.Uint16(vd[0:])
		if tag == 8 {
			break
		}
		if tag == 5 {
			v := binary.LittleEndian.Uint32(vd[188:])
			partitionStart = &v
		} else if tag == 6 {
			v := binary.LittleEndian.Uint32(vd[252:])
			fsdLBN = &v
		}
		lba++
	}

	if partitionStart == nil || fsdLBN == nil {
		return nil
	}

	fsd := make([]byte, scanStep)
	if _, err := f.ReadAt(fsd, int64(*partitionStart+*fsdLBN)*scanStep); err != nil {
		return nil
	}
	if binary.LittleEndian.Uint16(fsd[0:]) != 256 {
		return nil
	}
	rootLBN := binary.LittleEndian.Uint32(fsd[404:])

	var parts []string
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			parts = append(parts, strings.ToUpper(p))
		}
	}

	state := &udfState{f: f, partitionStart: *partitionStart}
	defer func() { recover() }() //nolint:errcheck — ignore malformed UDF structures
	return state.walk(rootLBN, parts)
}

// isoWalkDir walks an ISO 9660 / Joliet directory tree looking for parts.
func isoWalkDir(f *os.File, dirLBA uint32, dirSize int, parts []string, joliet bool) *fileExtent {
	data := make([]byte, dirSize)
	if _, err := f.ReadAt(data, int64(dirLBA)*scanStep); err != nil {
		return nil
	}
	pos := 0
	for pos < len(data) {
		recLen := int(data[pos])
		if recLen == 0 {
			pos = (pos/scanStep + 1) * scanStep
			if pos >= len(data) {
				break
			}
			continue
		}
		fileLBA := binary.LittleEndian.Uint32(data[pos+2:])
		fileSize := binary.LittleEndian.Uint32(data[pos+10:])
		flags := data[pos+25]
		nameLen := int(data[pos+32])
		if pos+33+nameLen > len(data) {
			break
		}
		raw := data[pos+33 : pos+33+nameLen]
		var name string
		if joliet {
			name = strings.ToUpper(utf16BEDecode(raw))
		} else {
			name = strings.ToUpper(string(raw))
		}
		if idx := strings.Index(name, ";"); idx >= 0 {
			name = name[:idx]
		}
		if name == parts[0] {
			if len(parts) == 1 {
				return &fileExtent{lba: fileLBA, size: uint64(fileSize)}
			}
			if flags&0x02 != 0 {
				return isoWalkDir(f, fileLBA, int(fileSize), parts[1:], joliet)
			}
		}
		pos += recLen
	}
	return nil
}

// isoRoot describes the location of a root directory found in a primary or
// supplementary (Joliet) volume descriptor.
type isoRoot struct {
	lba  uint32
	size int
}

// isoRoots scans the volume descriptors for the ISO 9660 primary root and,
// if present, the Joliet supplementary root.
func isoRoots(f *os.File) (primary, joliet *isoRoot) {
	vd := make([]byte, scanStep)
	for sector := 16; sector < 32; sector++ {
		if _, err := f.ReadAt(vd, int64(sector)*scanStep); err != nil {
			break
		}
		if vd[0] == 255 {
			break
		}
		lba := binary.LittleEndian.Uint32(vd[156+2:])
		size := int(binary.LittleEndian.Uint32(vd[156+10:]))
		if vd[0] == 1 && primary == nil {
			primary = &isoRoot{lba: lba, size: size}
		} else if vd[0] == 2 {
			esc := string(vd[88:91])
			if esc == "%/@" || esc == "%/C" || esc == "%/E" {
				joliet = &isoRoot{lba: lba, size: size}
			}
		}
	}
	return primary, joliet
}

// isoFindFile returns the extent for path, trying UDF then Joliet then ISO 9660.
func isoFindFile(f *os.File, path string) *fileExtent {
	if r := udfFindFile(f, path); r != nil {
		return r
	}

	primaryRoot, jolietRoot := isoRoots(f)

	var parts []string
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			parts = append(parts, strings.ToUpper(p))
		}
	}

	if jolietRoot != nil {
		if r := isoWalkDir(f, jolietRoot.lba, jolietRoot.size, parts, true); r != nil {
			return r
		}
	}
	if primaryRoot != nil {
		return isoWalkDir(f, primaryRoot.lba, primaryRoot.size, parts, false)
	}
	return nil
}

// ExtractISO extracts every file from srcPath's Joliet tree (falling back to
// the plain ISO 9660 tree if no Joliet volume descriptor is present) into
// destDir, preserving directory structure. It is exported so other
// datasources in this module (e.g. debloat) that need a full extracted copy
// of a source ISO's media can reuse this package's dependency-free ISO
// reader instead of shelling out to an external tool.
func ExtractISO(srcPath, destDir string) error {
	return isoExtractAll(srcPath, destDir)
}

// isoExtractAll extracts every file from the ISO's Joliet tree (falling back
// to the plain ISO 9660 tree if no Joliet volume descriptor is present) into
// destDir, preserving directory structure.
func isoExtractAll(srcPath, destDir string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()

	primaryRoot, jolietRoot := isoRoots(f)
	if jolietRoot != nil {
		return isoExtractDir(f, jolietRoot.lba, jolietRoot.size, true, destDir)
	}
	if primaryRoot != nil {
		return isoExtractDir(f, primaryRoot.lba, primaryRoot.size, false, destDir)
	}
	return fmt.Errorf("no ISO 9660 root directory found")
}

// isoExtractDir recursively extracts the directory at dirLBA into destDir.
func isoExtractDir(f *os.File, dirLBA uint32, dirSize int, joliet bool, destDir string) error {
	data := make([]byte, dirSize)
	if _, err := f.ReadAt(data, int64(dirLBA)*scanStep); err != nil {
		return err
	}
	pos := 0
	for pos < len(data) {
		recLen := int(data[pos])
		if recLen == 0 {
			pos = (pos/scanStep + 1) * scanStep
			if pos >= len(data) {
				break
			}
			continue
		}
		fileLBA := binary.LittleEndian.Uint32(data[pos+2:])
		fileSize := binary.LittleEndian.Uint32(data[pos+10:])
		flags := data[pos+25]
		nameLen := int(data[pos+32])
		if pos+33+nameLen > len(data) {
			break
		}
		raw := data[pos+33 : pos+33+nameLen]
		isDir := flags&0x02 != 0

		if nameLen == 1 && (raw[0] == 0x00 || raw[0] == 0x01) {
			pos += recLen
			continue
		}

		var name string
		if joliet {
			name = utf16BEDecode(raw)
		} else {
			name = string(raw)
		}
		if !isDir {
			if idx := strings.Index(name, ";"); idx >= 0 {
				name = name[:idx]
			}
		}

		outPath := filepath.Join(destDir, name)
		if isDir {
			if err := os.MkdirAll(outPath, 0o755); err != nil {
				return err
			}
			if err := isoExtractDir(f, fileLBA, int(fileSize), joliet, outPath); err != nil {
				return err
			}
		} else if err := isoExtractFile(f, fileLBA, fileSize, outPath); err != nil {
			return err
		}
		pos += recLen
	}
	return nil
}

func isoExtractFile(f *os.File, lba uint32, size uint32, outPath string) error {
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()
	if size == 0 {
		return nil
	}
	_, err = io.CopyN(out, io.NewSectionReader(f, int64(lba)*scanStep, int64(size)), int64(size))
	return err
}

// patchEFI writes data (padded with zeros to the next sector boundary) at
// lba*scanStep in out, replacing at most size bytes of allocated space.
func patchEFI(out *os.File, label string, lba uint32, size uint64, data []byte) {
	alloc := int((size + scanStep - 1) &^ (scanStep - 1))
	if len(data) > alloc {
		log.Printf("  WARNING: noprompt data (%d) > %s allocated (%d), skipping", len(data), label, alloc)
		return
	}
	buf := make([]byte, alloc)
	copy(buf, data)
	if _, err := out.WriteAt(buf, int64(lba)*scanStep); err != nil {
		log.Printf("  WARNING: failed to patch %s: %v", label, err)
		return
	}
	log.Printf("  %s patched at LBA %d", label, lba)
}

func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// patchISO copies srcPath to a temp file, optionally applying the enabled
// patch steps to the copy. It returns the path to the copy.
//
// If extraDrivers is non-empty, patching instead goes through a full
// extract/modify/repack pipeline (see patchISORebuild), since injected
// drivers change the size of boot.wim/install.wim beyond what fits back into
// the ISO's original fixed sector layout.
func patchISO(srcPath string, patchNoBoot bool, extraDrivers []string) (string, error) {
	if len(extraDrivers) > 0 {
		return patchISORebuild(srcPath, patchNoBoot, extraDrivers)
	}

	dstFile, err := os.CreateTemp("", "patched-*.iso")
	if err != nil {
		return "", fmt.Errorf("creating temp ISO: %w", err)
	}
	dstPath := dstFile.Name()
	dstFile.Close()

	log.Printf("  Copying ISO to %s ...", dstPath)
	if err := copyFileContents(srcPath, dstPath); err != nil {
		os.Remove(dstPath)
		return "", fmt.Errorf("copying ISO: %w", err)
	}

	if patchNoBoot {
		if err := patchNoBootPrompt(srcPath, dstPath); err != nil {
			os.Remove(dstPath)
			return "", err
		}
	}

	log.Printf("  Done — patched ISO ready.")
	return dstPath, nil
}

// noBootPromptData holds the no-prompt EFI boot image replacements located
// within an ISO by findNoBootPromptData.
type noBootPromptData struct {
	efiLBA       uint32
	efisysSize   int
	nopromptData []byte
	cdboot       *fileExtent
	cdbootNPData []byte
}

// findNoBootPromptData locates the no-prompt efisys.bin and cdboot.efi
// replacement data within srcPath.
func findNoBootPromptData(srcPath string) (*noBootPromptData, error) {
	efiLBA, err := eltoritoEFILBA(srcPath)
	if err != nil {
		return nil, err
	}
	log.Printf("  EFI boot image at LBA %d (offset %#x)", efiLBA, int64(efiLBA)*scanStep)

	f, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	orig := make([]byte, 1474560)
	if _, err := f.ReadAt(orig, int64(efiLBA)*scanStep); err != nil && err != io.EOF {
		return nil, fmt.Errorf("reading EFI boot image: %w", err)
	}

	efisysSize := fatImageSize(orig)
	if efisysSize == 0 {
		return nil, fmt.Errorf("EFI image is not a FAT12 filesystem")
	}
	log.Printf("  efisys.bin size: %d bytes", efisysSize)

	origHash := fatBootx64Hash(orig[:efisysSize])
	log.Printf("  efisys.bin BOOTX64.EFI md5: %s", origHash)

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	isoSize := fi.Size()

	var nopromptData []byte
	sector := make([]byte, 512)
	for offset := int64(efiLBA)*scanStep + scanStep; offset < isoSize; offset += scanStep {
		n, _ := f.ReadAt(sector, offset)
		if n < 16 {
			break
		}
		if sector[0] == 0xEB && sector[2] == 0x90 && string(sector[3:11]) == "MSDOS5.0" {
			bps := int(binary.LittleEndian.Uint16(sector[11:]))
			tot := int(binary.LittleEndian.Uint16(sector[19:]))
			if tot == 0 {
				tot = int(binary.LittleEndian.Uint32(sector[32:]))
			}
			if tot*bps == efisysSize {
				cand := make([]byte, efisysSize)
				if _, err := f.ReadAt(cand, offset); err != nil {
					continue
				}
				h := fatBootx64Hash(cand)
				if h != "" && h != origHash {
					nopromptData = cand
					log.Printf("  efisys_noprompt.bin at offset %#x (md5: %s)", offset, h)
					break
				}
			}
		}
	}
	if nopromptData == nil {
		return nil, fmt.Errorf("efisys_noprompt.bin not found in ISO")
	}

	cdbootNP := isoFindFile(f, "EFI/MICROSOFT/BOOT/CDBOOT_NOPROMPT.EFI")
	cdboot := isoFindFile(f, "EFI/MICROSOFT/BOOT/CDBOOT.EFI")

	if cdbootNP == nil {
		return nil, fmt.Errorf("cdboot_noprompt.efi not found in ISO")
	}
	log.Printf("  cdboot_noprompt.efi at LBA %d, size %d", cdbootNP.lba, cdbootNP.size)

	cdbootNPData := make([]byte, cdbootNP.size)
	if _, err := f.ReadAt(cdbootNPData, int64(cdbootNP.lba)*scanStep); err != nil {
		return nil, fmt.Errorf("reading cdboot_noprompt.efi: %w", err)
	}

	return &noBootPromptData{
		efiLBA:       efiLBA,
		efisysSize:   efisysSize,
		nopromptData: nopromptData,
		cdboot:       cdboot,
		cdbootNPData: cdbootNPData,
	}, nil
}

// patchNoBootPrompt replaces the EFI boot images (efisys.bin and cdboot.efi)
// in dstPath with their no-prompt variants found within srcPath.
func patchNoBootPrompt(srcPath, dstPath string) error {
	d, err := findNoBootPromptData(srcPath)
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dstPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer out.Close()

	patchEFI(out, "efisys.bin", d.efiLBA, uint64(d.efisysSize), d.nopromptData)
	if d.cdboot != nil {
		patchEFI(out, "cdboot.efi", d.cdboot.lba, d.cdboot.size, d.cdbootNPData)
	} else {
		log.Printf("  WARNING: cdboot.efi not found in ISO")
	}

	return nil
}

// patchNoBootPromptDir replaces the extracted efisys.bin and cdboot.efi files
// under mediaDir with their no-prompt variants found within srcPath.
func patchNoBootPromptDir(srcPath, mediaDir string) error {
	d, err := findNoBootPromptData(srcPath)
	if err != nil {
		return err
	}

	efisysPath, err := findByBasename(mediaDir, "efisys.bin")
	if err != nil {
		return fmt.Errorf("locating extracted efisys.bin: %w", err)
	}
	if err := os.WriteFile(efisysPath, d.nopromptData, 0o644); err != nil {
		return fmt.Errorf("writing efisys.bin: %w", err)
	}
	log.Printf("  efisys.bin replaced with no-prompt variant")

	if d.cdboot != nil {
		cdbootPath, err := findByBasename(mediaDir, "cdboot.efi")
		if err != nil {
			return fmt.Errorf("locating extracted cdboot.efi: %w", err)
		}
		if err := os.WriteFile(cdbootPath, d.cdbootNPData, 0o644); err != nil {
			return fmt.Errorf("writing cdboot.efi: %w", err)
		}
		log.Printf("  cdboot.efi replaced with no-prompt variant")
	} else {
		log.Printf("  WARNING: cdboot.efi not found in ISO")
	}

	return nil
}
