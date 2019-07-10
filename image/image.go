package image

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"

	"github.com/NeowayLabs/drm"
	"github.com/NeowayLabs/drm/mode"
	"launchpad.net/gommap"
)

type framebuffer struct {
	id     uint32
	handle uint32
	data   []byte
	fb     *mode.FB
	size   uint64
	stride uint32
}

type BGR565 struct {
	Pix    []uint8
	Stride int
	Rect   image.Rectangle
}

func (p *BGR565) Bounds() image.Rectangle { return p.Rect }
func (p *BGR565) ColorModel() color.Model { return color.NRGBAModel }
func (p *BGR565) PixOffset(x, y int) int  { return y*p.Stride + x*2 }

func (p *BGR565) Set(x, y int, c color.Color) {
	if !(image.Point{x, y}.In(p.Rect)) {
		return
	}
	i := p.PixOffset(x, y)
	c1 := color.NRGBAModel.Convert(c).(color.NRGBA)
	p.Pix[i+0] = (c1.B >> 3) | ((c1.G >> 2) << 5)
	p.Pix[i+1] = (c1.G >> 5) | ((c1.R >> 3) << 3)
}

func (p *BGR565) At(x, y int) color.Color {
	if !(image.Point{x, y}.In(p.Rect)) {
		return color.NRGBA{}
	}
	i := p.PixOffset(x, y)
	return color.NRGBA{(p.Pix[i+1] >> 3) << 3, (p.Pix[i+1] << 5) | ((p.Pix[i+0] >> 5) << 2), p.Pix[i+0] << 3, 255}
}

func createFramebuffer(file *os.File, dev *mode.Modeset) (framebuffer, error) {
	fb, err := mode.CreateFB(file, dev.Width, dev.Height, 32)
	if err != nil {
		return framebuffer{}, fmt.Errorf("failed to create framebuffer: %s", err.Error())
	}
	stride := fb.Pitch
	size := fb.Size
	handle := fb.Handle

	fbID, err := mode.AddFB(file, dev.Width, dev.Height, 24, 32, stride, handle)
	if err != nil {
		return framebuffer{}, fmt.Errorf("cannot create dumb buffer: %s", err.Error())
	}

	offset, err := mode.MapDumb(file, handle)
	if err != nil {
		return framebuffer{}, err
	}

	mmap, err := gommap.MapAt(0, uintptr(file.Fd()), int64(offset), int64(size), gommap.PROT_READ|gommap.PROT_WRITE, gommap.MAP_SHARED)
	if err != nil {
		return framebuffer{}, fmt.Errorf("failed to mmap framebuffer: %s", err.Error())
	}

	for i := uint64(0); i < size; i++ {
		mmap[i] = 0
	}

	framebuf := framebuffer{
		id:     fbID,
		handle: handle,
		data:   mmap,
		fb:     fb,
		size:   size,
		stride: stride,
	}

	return framebuf, nil
}

func NewDRMImage(drmIndex int) (draw.Image, error) {
	file, err := drm.OpenCard(drmIndex)
	if err != nil {
		return nil, fmt.Errorf("OpenCard(): %s", err.Error())
	}

	defer file.Close()
	if !drm.HasDumbBuffer(file) {
		return nil, fmt.Errorf("drm device does not support dumb buffers")
	}

	modeset, err := mode.NewSimpleModeset(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err.Error())
		os.Exit(1)
	}

	for _, mod := range modeset.Modesets {
		framebuf, err := createFramebuffer(file, &mod)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s\n", err.Error())
			// cleanup(modeset, msets, file)
			continue
		}

		// save current CRTC of this mode to restore at exit
		_, err = mode.GetCrtc(file, mod.Crtc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: Cannot get CRTC for connector %d: %s", mod.Conn, err.Error())
			// cleanup(modeset, msets, file)
			continue
		}
		// change the mode
		err = mode.SetCrtc(file, mod.Crtc, framebuf.id, 0, 0, &mod.Conn, 1, &mod.Mode)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Cannot set CRTC for connector %d: %s", mod.Conn, err.Error())
			// cleanup(modeset, msets, file)
			continue
		}

		return &BGR565{
			Pix:    framebuf.data,
			Stride: int(framebuf.stride),
			Rect:   image.Rect(0, 0, int(mod.Width), int(mod.Height)),
		}, nil
	}

	return nil, errors.New("Unable to find any modeset for framebuffer")
}
