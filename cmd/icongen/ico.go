package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
)

// encodePNG encodes img with a fixed compression level so output is stable.
func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeICO builds an ICO file whose entries are embedded PNG images. Sizes
// of 256 are stored as 0 in the width/height bytes, as the format requires.
func encodeICO(sizes []int, imageFor func(size int) image.Image) ([]byte, error) {
	pngs := make([][]byte, len(sizes))
	for i, size := range sizes {
		data, err := encodePNG(imageFor(size))
		if err != nil {
			return nil, err
		}
		pngs[i] = data
	}

	var out bytes.Buffer
	// ICONDIR: reserved, type (1 = icon), count.
	header := []uint16{0, 1, uint16(len(sizes))}
	if err := binary.Write(&out, binary.LittleEndian, header); err != nil {
		return nil, err
	}

	offset := 6 + 16*len(sizes)
	for i, size := range sizes {
		dim := byte(size)
		if size >= 256 {
			dim = 0
		}
		entry := struct {
			Width, Height, Colors, Reserved byte
			Planes, BitCount                uint16
			BytesInRes, ImageOffset         uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(pngs[i])), uint32(offset)}
		if err := binary.Write(&out, binary.LittleEndian, entry); err != nil {
			return nil, err
		}
		offset += len(pngs[i])
	}
	for _, data := range pngs {
		out.Write(data)
	}
	return out.Bytes(), nil
}
