package mascotmedia

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"

	"github.com/disintegration/imaging"
)

const MaxBytes = 8 << 20
const maxPixels = 16_000_000

// Prepare validates image contents and strips EXIF, comments, embedded names
// and trailing bytes while preserving the camera orientation.
func Prepare(data []byte) ([]byte, string, error) {
	if len(data) > MaxBytes {
		return nil, "", errors.New("mascot image exceeds 8 MiB")
	}
	mime := http.DetectContentType(data)
	if mime != "image/jpeg" && mime != "image/png" {
		return nil, "", errors.New("expected a JPEG or PNG image")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 8192 || config.Height > 8192 ||
		config.Width*config.Height > maxPixels {
		return nil, "", errors.New("invalid or oversized mascot image dimensions")
	}
	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, "", errors.New("invalid mascot image data")
	}
	var out boundedBuffer
	if mime == "image/png" {
		err = png.Encode(&out, img)
	} else {
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 90})
	}
	if err != nil {
		return nil, "", err
	}
	return out.buf.Bytes(), mime, nil
}

type boundedBuffer struct {
	buf bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > MaxBytes-b.buf.Len() {
		return 0, errors.New("normalized mascot image exceeds 8 MiB")
	}
	return b.buf.Write(p)
}
