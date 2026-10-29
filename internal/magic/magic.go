// Package magic classifies files by their leading bytes so that sendaway can
// refuse to bring native binaries and archives back to the local machine.
package magic

import (
	"bytes"
	"errors"
	"io"
	"os"
)

// Reasons returned by Classify. They are the values of the reason field in the exit frame.
const (
	ReasonELF     = "elf"
	ReasonMachO   = "macho"
	ReasonPE      = "pe"
	ReasonAr      = "ar"
	ReasonArchive = "archive"
)

// HeadLen is the number of leading bytes Classify inspects. It covers the tar magic at offset 257.
const HeadLen = 262

const tarOffset = 257

type signature struct {
	magic  []byte
	reason string
}

// signatures is checked in order; all entries are prefix matches.
var signatures = []signature{
	{[]byte{0x7F, 'E', 'L', 'F'}, ReasonELF},
	{[]byte{0xFE, 0xED, 0xFA, 0xCE}, ReasonMachO},
	{[]byte{0xFE, 0xED, 0xFA, 0xCF}, ReasonMachO},
	{[]byte{0xCE, 0xFA, 0xED, 0xFE}, ReasonMachO},
	{[]byte{0xCF, 0xFA, 0xED, 0xFE}, ReasonMachO},
	{[]byte{0xCA, 0xFE, 0xBA, 0xBE}, ReasonMachO},
	{[]byte{0xCA, 0xFE, 0xBA, 0xBF}, ReasonMachO},
	{[]byte{0xBE, 0xBA, 0xFE, 0xCA}, ReasonMachO},
	{[]byte{0xBF, 0xBA, 0xFE, 0xCA}, ReasonMachO},
	{[]byte{0x4D, 0x5A}, ReasonPE},
	{[]byte("!<arch>\n"), ReasonAr},
	{[]byte{0x50, 0x4B, 0x03, 0x04}, ReasonArchive},
	{[]byte{0x1F, 0x8B}, ReasonArchive},
	{[]byte{0xFD, 0x37, 0x7A, 0x58, 0x5A, 0x00}, ReasonArchive},
	{[]byte{0x28, 0xB5, 0x2F, 0xFD}, ReasonArchive},
	{[]byte{0x42, 0x5A, 0x68}, ReasonArchive},
}

var tarMagic = []byte("ustar")

// Classify returns the reason a file with the given leading bytes is refused, or "" when
// its content is not recognised as native or archive data (WASM and text pass). head may be
// shorter than HeadLen; a short head simply matches fewer signatures.
func Classify(head []byte) string {
	for _, s := range signatures {
		if bytes.HasPrefix(head, s.magic) {
			return s.reason
		}
	}
	end := tarOffset + len(tarMagic)
	if len(head) >= end && bytes.Equal(head[tarOffset:end], tarMagic) {
		return ReasonArchive
	}
	return ""
}

// ClassifyReader reads at most HeadLen bytes from r and classifies them. It returns an
// error only for read failures; end of input is not an error.
func ClassifyReader(r io.Reader) (string, error) {
	buf := make([]byte, HeadLen)
	n, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	return Classify(buf[:n]), nil
}

// ClassifyFile opens path and classifies its leading bytes. It follows symlinks; callers
// that must not follow them check the file type before calling.
func ClassifyFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	return ClassifyReader(f)
}

// Mode selects which reasons a collection refuses.
type Mode int

const (
	// Pull refuses native binaries and archives.
	Pull Mode = iota
	// Artifacts refuses native binaries only; archives are collected.
	Artifacts
)

// Refused reports whether a file with the given reason must not be collected in mode m.
// An empty reason is never refused.
func Refused(m Mode, reason string) bool {
	switch reason {
	case "":
		return false
	case ReasonArchive:
		return m == Pull
	default:
		return true
	}
}
