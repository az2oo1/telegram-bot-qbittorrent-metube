package telegram

import (
	"encoding/binary"
	"testing"
)

func TestParseMP4Metadata(t *testing.T) {
	// mvhd box: size=32, name="mvhd", version=0, timescale=1000, duration=5000 (5s)
	mvhd := make([]byte, 32)
	binary.BigEndian.PutUint32(mvhd[0:4], 32)
	copy(mvhd[4:8], "mvhd")
	mvhd[8] = 0 // version 0
	binary.BigEndian.PutUint32(mvhd[20:24], 1000) // timescale at payload offset 12
	binary.BigEndian.PutUint32(mvhd[24:28], 5000) // duration at payload offset 16

	// tkhd box: size=92, name="tkhd", version=0, width=1920, height=1080
	tkhd := make([]byte, 92)
	binary.BigEndian.PutUint32(tkhd[0:4], 92)
	copy(tkhd[4:8], "tkhd")
	tkhd[8] = 0 // version 0
	binary.BigEndian.PutUint32(tkhd[84:88], 1920<<16) // width at payload offset 76
	binary.BigEndian.PutUint32(tkhd[88:92], 1080<<16) // height at payload offset 80

	// trak box containing tkhd
	trakSize := 8 + len(tkhd)
	trak := make([]byte, trakSize)
	binary.BigEndian.PutUint32(trak[0:4], uint32(trakSize))
	copy(trak[4:8], "trak")
	copy(trak[8:], tkhd)

	// moov box containing mvhd and trak
	moovSize := 8 + len(mvhd) + len(trak)
	moov := make([]byte, moovSize)
	binary.BigEndian.PutUint32(moov[0:4], uint32(moovSize))
	copy(moov[4:8], "moov")
	copy(moov[8:], mvhd)
	copy(moov[8+len(mvhd):], trak)

	// mp4 data with ftyp + moov
	ftyp := make([]byte, 16)
	binary.BigEndian.PutUint32(ftyp[0:4], 16)
	copy(ftyp[4:8], "ftyp")

	data := append(ftyp, moov...)

	dur, w, h := parseMP4Metadata(data)
	if dur != 5 {
		t.Errorf("expected duration 5, got %d", dur)
	}
	if w != 1920 {
		t.Errorf("expected width 1920, got %d", w)
	}
	if h != 1080 {
		t.Errorf("expected height 1080, got %d", h)
	}
}

func TestEscapeMarkdown(t *testing.T) {
	input := "Hello_world*test[1]2`"
	expected := "Hello\\_world\\*test\\[1\\]2\\`"
	if res := EscapeMarkdown(input); res != expected {
		t.Errorf("expected %s, got %s", expected, res)
	}
}
