// Command winres wraps an application manifest in a COFF object (.syso) that the Go linker embeds
// as resource RT_MANIFEST #1, so the Windows build needs no cgo or resource compiler.
//
//	go run ./tools/winres <manifest> <out.syso>
//
// The object holds one .rsrc section: a three-level resource directory (type → name → language)
// ending in a data entry that points at the manifest bytes. That pointer is an RVA the linker fills
// in, so the object also carries one relocation and one section symbol for it to resolve against.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
)

const (
	machineAMD64          = 0x8664
	sectionCharacteristic = 0x40000040 // IMAGE_SCN_CNT_INITIALIZED_DATA | IMAGE_SCN_MEM_READ
	relocationAddr32NB    = 3          // IMAGE_REL_AMD64_ADDR32NB: a 32-bit RVA
	symbolClassStatic     = 3          // IMAGE_SYM_CLASS_STATIC

	resourceTypeManifest = 24
	manifestResourceID   = 1
	languageEnglishUS    = 0x409
	pointsToSubdirectory = 0x80000000 // high bit of a directory entry's target

	// Offsets inside the .rsrc section. Each directory is a 16-byte header plus one 8-byte entry.
	typeDirectoryOffset     = 0
	nameDirectoryOffset     = 24
	languageDirectoryOffset = 48
	dataEntryOffset         = 72
	manifestOffset          = dataEntryOffset + 16
)

var sectionName = [8]byte{'.', 'r', 's', 'r', 'c'}

type coffHeader struct {
	Machine              uint16
	NumberOfSections     uint16
	TimeDateStamp        uint32
	PointerToSymbolTable uint32
	NumberOfSymbols      uint32
	SizeOfOptionalHeader uint16
	Characteristics      uint16
}

type sectionHeader struct {
	Name                 [8]byte
	VirtualSize          uint32
	VirtualAddress       uint32
	SizeOfRawData        uint32
	PointerToRawData     uint32
	PointerToRelocations uint32
	PointerToLinenumbers uint32
	NumberOfRelocations  uint16
	NumberOfLinenumbers  uint16
	Characteristics      uint32
}

type relocation struct {
	VirtualAddress   uint32
	SymbolTableIndex uint32
	Type             uint16
}

type symbol struct {
	Name               [8]byte
	Value              uint32
	SectionNumber      int16
	Type               uint16
	StorageClass       uint8
	NumberOfAuxSymbols uint8
}

type resourceDirectory struct {
	Characteristics      uint32
	TimeDateStamp        uint32
	MajorVersion         uint16
	MinorVersion         uint16
	NumberOfNamedEntries uint16
	NumberOfIDEntries    uint16
}

type resourceDirectoryEntry struct {
	ID     uint32
	Target uint32 // offset of a subdirectory (with pointsToSubdirectory set) or of a data entry
}

type resourceDataEntry struct {
	DataRVA  uint32 // holds the offset in the section; the linker adds the section's RVA
	Size     uint32
	CodePage uint32
	Reserved uint32
}

const (
	coffHeaderSize    = 20
	sectionHeaderSize = 40
	relocationSize    = 10
	emptyStringTable  = uint32(4) // the string table's size field counts itself
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: winres <manifest> <out.syso>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(manifestPath, outPath string) error {
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	return os.WriteFile(outPath, buildObject(manifest), 0o644)
}

func buildObject(manifest []byte) []byte {
	section := buildResourceSection(manifest)

	rawDataOffset := uint32(coffHeaderSize + sectionHeaderSize)
	relocationsOffset := rawDataOffset + uint32(len(section))
	symbolTableOffset := relocationsOffset + relocationSize

	var out bytes.Buffer
	write(&out, coffHeader{
		Machine:              machineAMD64,
		NumberOfSections:     1,
		PointerToSymbolTable: symbolTableOffset,
		NumberOfSymbols:      1,
	})
	write(&out, sectionHeader{
		Name:                 sectionName,
		SizeOfRawData:        uint32(len(section)),
		PointerToRawData:     rawDataOffset,
		PointerToRelocations: relocationsOffset,
		NumberOfRelocations:  1,
		Characteristics:      sectionCharacteristic,
	})
	out.Write(section)
	write(&out, relocation{
		VirtualAddress:   dataEntryOffset,
		SymbolTableIndex: 0,
		Type:             relocationAddr32NB,
	})
	write(&out, symbol{
		Name:          sectionName,
		SectionNumber: 1,
		StorageClass:  symbolClassStatic,
	})
	write(&out, emptyStringTable)
	return out.Bytes()
}

func buildResourceSection(manifest []byte) []byte {
	var section bytes.Buffer
	writeDirectoryWithOneEntry(&section, resourceTypeManifest, pointsToSubdirectory|nameDirectoryOffset)
	writeDirectoryWithOneEntry(&section, manifestResourceID, pointsToSubdirectory|languageDirectoryOffset)
	writeDirectoryWithOneEntry(&section, languageEnglishUS, dataEntryOffset)
	write(&section, resourceDataEntry{
		DataRVA: manifestOffset,
		Size:    uint32(len(manifest)),
	})
	section.Write(manifest)
	for section.Len()%8 != 0 {
		section.WriteByte(0)
	}
	return section.Bytes()
}

func writeDirectoryWithOneEntry(section *bytes.Buffer, id, target uint32) {
	write(section, resourceDirectory{NumberOfIDEntries: 1})
	write(section, resourceDirectoryEntry{ID: id, Target: target})
}

// write appends v in little-endian; writing to a bytes.Buffer cannot fail.
func write(buf *bytes.Buffer, v any) {
	binary.Write(buf, binary.LittleEndian, v)
}
