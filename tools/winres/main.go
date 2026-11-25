// Command winres wraps an application manifest in a COFF object (.syso) that the Go linker embeds
// as resource RT_MANIFEST #1, so the Windows build needs no cgo or resource compiler.
//
//	go run ./tools/winres <manifest> <out.syso>
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
)

const (
	rtManifest     = 24
	langEnUS       = 0x409
	subdirFlag     = 0x80000000
	relAddr32NB    = 3 // IMAGE_REL_AMD64_ADDR32NB: the data entry holds an RVA
	scnFlags       = 0x40000040
	machineAMD64   = 0x8664
	coffHeaderSize = 20
	sectionHdrSize = 40
	dataEntryOff   = 72 // three 16-byte directories, each followed by one 8-byte entry
	manifestOff    = dataEntryOff + 16
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: winres <manifest> <out.syso>")
		os.Exit(2)
	}
	manifest, err := os.ReadFile(os.Args[1])
	if err == nil {
		err = os.WriteFile(os.Args[2], build(manifest), 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func build(manifest []byte) []byte {
	le := binary.LittleEndian
	var sec bytes.Buffer
	dir := func(id, target uint32) {
		binary.Write(&sec, le, [4]uint32{0, 0, 0, 1 << 16}) // characteristics, time, version, 0 named + 1 id entry
		binary.Write(&sec, le, [2]uint32{id, target})
	}
	dir(rtManifest, subdirFlag|24)
	dir(1, subdirFlag|48)
	dir(langEnUS, dataEntryOff)
	binary.Write(&sec, le, [4]uint32{manifestOff, uint32(len(manifest)), 0, 0}) // RVA addend, size, codepage, reserved
	sec.Write(manifest)
	for sec.Len()%8 != 0 {
		sec.WriteByte(0)
	}

	rawOff := uint32(coffHeaderSize + sectionHdrSize)
	relocOff := rawOff + uint32(sec.Len())
	symOff := relocOff + 10

	var out bytes.Buffer
	binary.Write(&out, le, struct {
		Machine, Sections              uint16
		Time, SymTable, Symbols        uint32
		OptHeaderSize, Characteristics uint16
	}{machineAMD64, 1, 0, symOff, 1, 0, 0})
	binary.Write(&out, le, struct {
		Name                                 [8]byte
		VirtualSize, VirtualAddress, RawSize uint32
		RawOff, RelocOff, LineOff            uint32
		Relocs, Lines                        uint16
		Characteristics                      uint32
	}{[8]byte{'.', 'r', 's', 'r', 'c'}, 0, 0, uint32(sec.Len()), rawOff, relocOff, 0, 1, 0, scnFlags})
	out.Write(sec.Bytes())
	binary.Write(&out, le, struct {
		Addr, Sym uint32
		Type      uint16
	}{dataEntryOff, 0, relAddr32NB})
	binary.Write(&out, le, struct {
		Name       [8]byte
		Value      uint32
		Section    int16
		Type       uint16
		Class, Aux uint8
	}{[8]byte{'.', 'r', 's', 'r', 'c'}, 0, 1, 0, 3, 0}) // section symbol, IMAGE_SYM_CLASS_STATIC
	binary.Write(&out, le, uint32(4)) // empty string table
	return out.Bytes()
}
