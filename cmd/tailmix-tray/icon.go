package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// The tray icon is a 3x3 dot grid, a nod to the Tailscale logo. Each row
// stands for one of the first three enabled tailnets and lights up in its
// own colour while that tailnet is connected — the "mix".
var rowColors = [3]color.NRGBA{
	{0x14, 0xb8, 0xa6, 0xff}, // teal
	{0x8b, 0x5c, 0xf6, 0xff}, // violet
	{0xf5, 0x9e, 0x0b, 0xff}, // amber
}

var (
	dimDot   = color.NRGBA{0x9c, 0xa3, 0xaf, 0xff}
	offDot   = color.NRGBA{0x9c, 0xa3, 0xaf, 0x80}
	badgeRed = color.NRGBA{0xef, 0x44, 0x44, 0xff}
	badgeYel = color.NRGBA{0xfa, 0xcc, 0x15, 0xff}
)

const iconSize = 64

// renderIcon draws the tray icon for m as a PNG image.
func renderIcon(m trayModel) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, iconSize, iconSize))
	const (
		pitch  = 21.0
		origin = 11.0
		radius = 8.5
	)
	for row := range 3 {
		for col := range 3 {
			c := dimDot
			switch {
			case m.Icon == iconOffline || m.Icon == iconDown:
				c = offDot
			case m.Lit[row]:
				c = rowColors[row]
			}
			fillCircle(img, origin+pitch*float64(col), origin+pitch*float64(row), radius, c)
		}
	}
	switch m.Icon {
	case iconError:
		fillCircle(img, 52, 52, 11, badgeRed)
	case iconStarting:
		fillCircle(img, 52, 52, 9, badgeYel)
	}
	return img
}

// fillCircle composites an anti-aliased disc onto img.
func fillCircle(img *image.NRGBA, cx, cy, r float64, c color.NRGBA) {
	minX, maxX := int(math.Floor(cx-r-1)), int(math.Ceil(cx+r+1))
	minY, maxY := int(math.Floor(cy-r-1)), int(math.Ceil(cy+r+1))
	for y := max(minY, 0); y < min(maxY, iconSize); y++ {
		for x := max(minX, 0); x < min(maxX, iconSize); x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			coverage := math.Max(0, math.Min(1, r-d+0.5))
			if coverage == 0 {
				continue
			}
			blend(img, x, y, c, coverage)
		}
	}
}

func blend(img *image.NRGBA, x, y int, c color.NRGBA, coverage float64) {
	dst := img.NRGBAAt(x, y)
	srcA := float64(c.A) / 255 * coverage
	dstA := float64(dst.A) / 255
	outA := srcA + dstA*(1-srcA)
	if outA == 0 {
		return
	}
	mix := func(s, d uint8) uint8 {
		return uint8(math.Round((float64(s)*srcA + float64(d)*dstA*(1-srcA)) / outA))
	}
	img.SetNRGBA(x, y, color.NRGBA{mix(c.R, dst.R), mix(c.G, dst.G), mix(c.B, dst.B), uint8(math.Round(outA * 255))})
}

// encodeICO wraps img as a single-image ICO file with an embedded PNG, which
// Windows Vista and later accept.
func encodeICO(img image.Image) ([]byte, error) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, img); err != nil {
		return nil, err
	}
	b := img.Bounds()
	var out bytes.Buffer
	header := struct {
		Reserved, Type, Count uint16
	}{0, 1, 1}
	entry := struct {
		Width, Height, Colors, Reserved uint8
		Planes, BitCount                uint16
		Size, Offset                    uint32
	}{
		Width: icoDim(b.Dx()), Height: icoDim(b.Dy()),
		Planes: 1, BitCount: 32,
		Size: uint32(pngData.Len()), Offset: 6 + 16,
	}
	if err := binary.Write(&out, binary.LittleEndian, header); err != nil {
		return nil, err
	}
	if err := binary.Write(&out, binary.LittleEndian, entry); err != nil {
		return nil, err
	}
	out.Write(pngData.Bytes())
	return out.Bytes(), nil
}

// icoDim encodes an ICO dimension, where 0 means 256.
func icoDim(n int) uint8 {
	if n >= 256 {
		return 0
	}
	return uint8(n)
}
