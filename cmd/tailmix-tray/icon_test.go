package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestRenderIconLightsRunningRows(t *testing.T) {
	m := renderIcon(trayModel{Icon: iconUp, Lit: [3]bool{true, false, true}})
	center := func(row int) (uint32, uint32, uint32) {
		r, g, b, _ := m.At(11, 11+21*row).RGBA()
		return r >> 8, g >> 8, b >> 8
	}
	if r, g, b := center(0); r != uint32(rowColors[0].R) || g != uint32(rowColors[0].G) || b != uint32(rowColors[0].B) {
		t.Fatalf("row 0 colour = %d,%d,%d, want teal", r, g, b)
	}
	if r, g, b := center(1); r != uint32(dimDot.R) || g != uint32(dimDot.G) || b != uint32(dimDot.B) {
		t.Fatalf("row 1 colour = %d,%d,%d, want dim", r, g, b)
	}
	if _, _, _, a := m.At(0, 0).RGBA(); a != 0 {
		t.Fatal("corner is not transparent")
	}
}

func TestEncodeICOEmbedsPNG(t *testing.T) {
	data, err := encodeICO(renderIcon(trayModel{Icon: iconOffline}))
	if err != nil {
		t.Fatal(err)
	}
	if typ := binary.LittleEndian.Uint16(data[2:4]); typ != 1 {
		t.Fatalf("ICO type = %d", typ)
	}
	size := binary.LittleEndian.Uint32(data[14:18])
	offset := binary.LittleEndian.Uint32(data[18:22])
	img, err := png.Decode(bytes.NewReader(data[offset : offset+size]))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != iconSize || data[6] != iconSize {
		t.Fatalf("icon size = %d (header %d)", img.Bounds().Dx(), data[6])
	}
}
