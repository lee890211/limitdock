//go:build ignore

package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	_ "image/png"
	"os"
)

func main() {
	outPath := flag.String("out", "", "output GIF path")
	delay := flag.Int("delay", 90, "frame delay in hundredths of a second")
	flag.Parse()
	if *outPath == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: make-slide-gif -out output.gif [-delay 90] frame.png...")
		os.Exit(2)
	}

	frames := make([]image.Image, 0, flag.NArg())
	maxX, maxY := 0, 0
	for _, path := range flag.Args() {
		img, err := readImage(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			os.Exit(1)
		}
		frames = append(frames, img)
		b := img.Bounds()
		if b.Dx() > maxX {
			maxX = b.Dx()
		}
		if b.Dy() > maxY {
			maxY = b.Dy()
		}
	}

	bg := image.NewUniform(color.RGBA{R: 17, G: 24, B: 32, A: 255})
	out := &gif.GIF{}
	canvas := image.Rect(0, 0, maxX, maxY)
	for _, img := range frames {
		rgba := image.NewRGBA(canvas)
		draw.Draw(rgba, canvas, bg, image.Point{}, draw.Src)
		draw.Draw(rgba, img.Bounds().Add(image.Point{}).Intersect(canvas), img, img.Bounds().Min, draw.Over)

		paletted := image.NewPaletted(canvas, palette.Plan9)
		draw.FloydSteinberg.Draw(paletted, canvas, rgba, image.Point{})
		out.Image = append(out.Image, paletted)
		out.Delay = append(out.Delay, *delay)
	}

	f, err := os.Create(*outPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	if err := gif.EncodeAll(f, out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func readImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}
