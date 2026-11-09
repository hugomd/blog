package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"strings"

	"github.com/disintegration/imaging"
	"github.com/rwcarlsen/goexif/exif"
)

type derivative struct {
	Size int
	Body []byte
}

func processImage(body []byte) (int, int, map[string]string, string, []derivative, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || (format != "jpeg" && format != "png") || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 40_000_000 || config.Width > 16000 || config.Height > 16000 {
		return 0, 0, nil, "", nil, badRequest("Use a JPEG or PNG up to 30 MB and 40 megapixels. Export HEIC/RAW files to JPEG first.")
	}
	img, err := imaging.Decode(bytes.NewReader(body), imaging.AutoOrientation(true))
	if err != nil {
		return 0, 0, nil, "", nil, badRequest("The image could not be decoded.")
	}
	metadata := map[string]string{}
	capturedDate := ""
	if fields, err := exif.Decode(bytes.NewReader(body)); err == nil {
		// Deliberately allowlist metadata. GPS, owner names and serial numbers stay private.
		for tag, label := range map[exif.FieldName]string{exif.Make: "Make", exif.Model: "Camera", exif.LensModel: "Lens"} {
			if value, err := fields.Get(tag); err == nil {
				if value, err := value.StringVal(); err == nil {
					metadata[label] = strings.TrimSpace(value)
				}
			}
		}
		for tag, label := range map[exif.FieldName]string{exif.FNumber: "Aperture", exif.ExposureTime: "Shutter", exif.FocalLength: "Focal length"} {
			if value, err := fields.Get(tag); err == nil {
				if n, d, err := value.Rat2(0); err == nil && d != 0 {
					switch tag {
					case exif.FNumber:
						metadata[label] = fmt.Sprintf("ƒ/%.1f", float64(n)/float64(d))
					case exif.ExposureTime:
						if n == 1 {
							metadata[label] = fmt.Sprintf("1/%d s", d)
						} else {
							metadata[label] = fmt.Sprintf("%.4g s", float64(n)/float64(d))
						}
					case exif.FocalLength:
						metadata[label] = fmt.Sprintf("%.0f mm", float64(n)/float64(d))
					}
				}
			}
		}
		if value, err := fields.Get(exif.ISOSpeedRatings); err == nil {
			if value, err := value.Int(0); err == nil {
				metadata["ISO"] = fmt.Sprint(value)
			}
		}
		if value, err := fields.DateTime(); err == nil {
			capturedDate = value.Format("2006-01-02")
		}
	}
	width, height := img.Bounds().Dx(), img.Bounds().Dy()
	var variants []derivative
	for _, size := range []int{640, 1280, 2560} {
		resized := img
		if width > size || height > size {
			resized = imaging.Fit(img, size, size, imaging.Lanczos)
		}
		// Flatten PNG transparency onto white instead of JPEG's implicit black.
		canvas := image.NewRGBA(resized.Bounds())
		draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		draw.Draw(canvas, canvas.Bounds(), resized, resized.Bounds().Min, draw.Over)
		var encoded bytes.Buffer
		if err := jpeg.Encode(&encoded, canvas, &jpeg.Options{Quality: 88}); err != nil {
			return 0, 0, nil, "", nil, err
		}
		// Fresh JPEG encoding contains no source EXIF or embedded location metadata.
		variants = append(variants, derivative{Size: size, Body: encoded.Bytes()})
	}
	return width, height, metadata, capturedDate, variants, nil
}
