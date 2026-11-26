package mascotmedia

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestPrepareStripsMetadataAndPreservesPortraitOrientation(t *testing.T) {
	var original bytes.Buffer
	if err := jpeg.Encode(&original, image.NewRGBA(image.Rect(0, 0, 3, 2)), nil); err != nil {
		t.Fatal(err)
	}
	// APP1 contains a little-endian EXIF orientation of 6 (rotate 90° clockwise).
	exif := []byte("Exif\x00\x00II\x2a\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00\x06\x00\x00\x00\x00\x00\x00\x00")
	data := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0, byte(len(exif) + 2)}, exif...)
	data = append(data, original.Bytes()[2:]...)
	data = append(data, []byte("AUTHOR METADATA")...)
	clean, mime, err := Prepare(data)
	if err != nil || mime != "image/jpeg" {
		t.Fatalf("Prepare: %s %v", mime, err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(clean))
	if err != nil || cfg.Width != 2 || cfg.Height != 3 || bytes.Contains(clean, exif) || bytes.Contains(clean, []byte("AUTHOR METADATA")) {
		t.Fatalf("unexpected image after normalization: %+v, %v", cfg, err)
	}
}

func TestPrepareAcceptsPNGAndRejectsInvalidUploads(t *testing.T) {
	var pngImage bytes.Buffer
	if err := png.Encode(&pngImage, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if clean, mime, err := Prepare(pngImage.Bytes()); err != nil || mime != "image/png" || len(clean) == 0 {
		t.Fatalf("valid PNG: %s %v", mime, err)
	}
	var wide bytes.Buffer
	if err := png.Encode(&wide, image.NewRGBA(image.Rect(0, 0, 8193, 1))); err != nil {
		t.Fatal(err)
	}
	for name, sample := range map[string][]byte{
		"svg": []byte("<svg/>"), "oversize": make([]byte, MaxBytes+1),
		"truncated": pngImage.Bytes()[:32], "wide": wide.Bytes(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Prepare(sample); err == nil {
				t.Error("rejected image accepted")
			}
		})
	}
}
