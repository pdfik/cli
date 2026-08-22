package main

import (
	"encoding/binary"
	"io"
	"os"
	"unicode/utf16"
	"unicode/utf8"
)

// readInput reads the HTML input ("-" = stdin) and normalises it to valid
// UTF-8 — the API treats every submission as UTF-8. Windows PowerShell 5.1
// writes UTF-16LE by default, which would otherwise be submitted as mojibake
// and burn a quota unit.
func readInput(arg string, stdin io.Reader) (string, error) {
	var b []byte
	var err error
	if arg == "-" {
		b, err = io.ReadAll(stdin)
	} else {
		b, err = os.ReadFile(arg)
	}
	if err != nil {
		return "", &usageError{err}
	}
	if len(b) >= 2 && ((b[0] == 0xFF && b[1] == 0xFE) || (b[0] == 0xFE && b[1] == 0xFF)) {
		return decodeUTF16(b, inputName(arg))
	}
	b = trimUTF8BOM(b) // a stray U+FEFF would otherwise render on the page
	if !utf8.Valid(b) {
		return "", usagef("%s is not valid UTF-8 — re-save it as UTF-8 (in PowerShell: Out-File -Encoding utf8)", inputName(arg))
	}
	return string(b), nil
}

// decodeUTF16 transcodes a BOM-prefixed UTF-16 document (standard library only).
func decodeUTF16(b []byte, name string) (string, error) {
	littleEndian := b[0] == 0xFF
	payload := b[2:]
	if len(payload)%2 != 0 {
		return "", usagef("%s looks UTF-16 encoded but has an odd byte length — re-save it as UTF-8", name)
	}
	units := make([]uint16, len(payload)/2)
	for i := range units {
		if littleEndian {
			units[i] = binary.LittleEndian.Uint16(payload[i*2:])
		} else {
			units[i] = binary.BigEndian.Uint16(payload[i*2:])
		}
	}
	return string(utf16.Decode(units)), nil
}

func trimUTF8BOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

func inputName(arg string) string {
	if arg == "-" {
		return "stdin"
	}
	return arg
}
