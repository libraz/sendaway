package magic

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"
)

func tarHead() []byte {
	head := make([]byte, HeadLen)
	copy(head[tarOffset:], "ustar\x0000")
	return head
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		head []byte
		want string
	}{
		{"elf", []byte{0x7F, 'E', 'L', 'F', 2, 1}, ReasonELF},
		{"macho 32 BE", []byte{0xFE, 0xED, 0xFA, 0xCE}, ReasonMachO},
		{"macho 64 BE", []byte{0xFE, 0xED, 0xFA, 0xCF}, ReasonMachO},
		{"macho 32 LE", []byte{0xCE, 0xFA, 0xED, 0xFE}, ReasonMachO},
		{"macho 64 LE", []byte{0xCF, 0xFA, 0xED, 0xFE}, ReasonMachO},
		{"fat", []byte{0xCA, 0xFE, 0xBA, 0xBE, 0, 0, 0, 2}, ReasonMachO},
		{"fat 64", []byte{0xCA, 0xFE, 0xBA, 0xBF}, ReasonMachO},
		{"fat LE", []byte{0xBE, 0xBA, 0xFE, 0xCA}, ReasonMachO},
		{"fat 64 LE", []byte{0xBF, 0xBA, 0xFE, 0xCA}, ReasonMachO},
		{"pe", []byte{0x4D, 0x5A, 0x90, 0x00}, ReasonPE},
		{"ar", []byte("!<arch>\nfoo"), ReasonAr},
		{"zip", []byte{0x50, 0x4B, 0x03, 0x04}, ReasonArchive},
		{"gzip", []byte{0x1F, 0x8B, 0x08}, ReasonArchive},
		{"xz", []byte{0xFD, 0x37, 0x7A, 0x58, 0x5A, 0x00}, ReasonArchive},
		{"zstd", []byte{0x28, 0xB5, 0x2F, 0xFD, 0x00}, ReasonArchive},
		{"bzip2", []byte{0x42, 0x5A, 0x68, 0x39}, ReasonArchive},
		{"tar", tarHead(), ReasonArchive},
		{"wasm", []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00}, ""},
		{"text", []byte("#!/bin/sh\necho hi\n"), ""},
		{"empty", nil, ""},
		{"one byte of elf", []byte{0x7F}, ""},
		{"one byte of ar", []byte{'!'}, ""},
		{"ar without newline", []byte("!<arch>"), ""},
		{"tar magic too short", tarHead()[:tarOffset+4], ""},
		{"tar magic absent", make([]byte, HeadLen), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.head); got != tt.want {
				t.Errorf("Classify(%q) = %q, want %q", tt.head, got, tt.want)
			}
		})
	}

	t.Run("reader one byte at a time", func(t *testing.T) {
		got, err := ClassifyReader(iotest.OneByteReader(bytes.NewReader([]byte{0x7F, 'E', 'L', 'F'})))
		if err != nil || got != ReasonELF {
			t.Errorf("ClassifyReader = %q, %v; want %q, nil", got, err, ReasonELF)
		}
	})
	t.Run("reader empty", func(t *testing.T) {
		got, err := ClassifyReader(bytes.NewReader(nil))
		if err != nil || got != "" {
			t.Errorf("ClassifyReader(empty) = %q, %v; want \"\", nil", got, err)
		}
	})
	t.Run("file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bin")
		if err := os.WriteFile(path, []byte{0xCF, 0xFA, 0xED, 0xFE}, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ClassifyFile(path)
		if err != nil || got != ReasonMachO {
			t.Errorf("ClassifyFile = %q, %v; want %q, nil", got, err, ReasonMachO)
		}
	})
}

func TestPullVsArtifacts(t *testing.T) {
	tests := []struct {
		reason    string
		pull      bool
		artifacts bool
	}{
		{ReasonELF, true, true},
		{ReasonMachO, true, true},
		{ReasonPE, true, true},
		{ReasonAr, true, true},
		{ReasonArchive, true, false},
		{"", false, false},
	}
	for _, tt := range tests {
		t.Run("reason="+tt.reason, func(t *testing.T) {
			if got := Refused(Pull, tt.reason); got != tt.pull {
				t.Errorf("Refused(Pull, %q) = %v, want %v", tt.reason, got, tt.pull)
			}
			if got := Refused(Artifacts, tt.reason); got != tt.artifacts {
				t.Errorf("Refused(Artifacts, %q) = %v, want %v", tt.reason, got, tt.artifacts)
			}
		})
	}
}
