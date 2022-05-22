package main

import (
	"fmt"
	"image"
	"math"

	"github.com/brianolson/raspi-mjpeg-server/jd"
)

func ensureSmall(x *jpegt, targetSize int) (err error) {
	sm, err := jd.Decimate(x.unpacked, targetSize)
	if err != nil {
		return
	}
	x.mini = sm
	return
}

var smallTargetSize int = 150

func smallDiff(old, new *jpegt) (score float64, err error) {
	err = ensureSmall(old, smallTargetSize)
	if err != nil {
		return
	}
	err = ensureSmall(new, smallTargetSize)
	if err != nil {
		return
	}
	return diffScore(old.mini, new.mini)
}

func diffScore(a, b image.Image) (score float64, err error) {
	aycbcr, ok := a.(*image.YCbCr)
	if !ok {
		err = fmt.Errorf("TODO: WRITEME diffScore %T", a)
		return
	}
	bycbcr, ok := b.(*image.YCbCr)
	if !ok {
		err = fmt.Errorf("TODO: WRITEME diffScore %T", b)
		return
	}
	return diffScoreYCbCr(aycbcr, bycbcr)
}

const yDiffMinThreshold = 20

type subsampleParts struct {
	rat image.YCbCrSubsampleRatio
	dy  int
	dx  int
}

var subsampleKey = []subsampleParts{
	subsampleParts{image.YCbCrSubsampleRatio444, 1, 1},
	subsampleParts{image.YCbCrSubsampleRatio422, 1, 2},
}

func getsk(im *image.YCbCr) subsampleParts {
	for _, sk := range subsampleKey {
		if sk.rat == im.SubsampleRatio {
			return sk
		}
	}
	return subsampleParts{0, 0, 0}
}

func polarize(x, y uint8) (r, th float64) {
	th = math.Atan2(float64(y), float64(x))
	r = math.Sqrt(float64((x * x) + (y * y)))
	return
}

func diffScoreYCbCr(a, b *image.YCbCr) (score float64, err error) {
	if a.YStride != b.YStride {
		err = fmt.Errorf("a.YStride (%d) != b.YStride (%d)", a.YStride, b.YStride)
		return
	}
	if a.CStride != b.CStride {
		err = fmt.Errorf("a.CStride (%d) != b.CStride (%d)", a.CStride, b.CStride)
		return
	}
	if a.SubsampleRatio != b.SubsampleRatio {
		err = fmt.Errorf("a.SubsampleRatio (%d) != b.SubsampleRatio (%d)", a.SubsampleRatio, b.SubsampleRatio)
		return
	}
	if a.Rect != b.Rect {
		err = fmt.Errorf("a.Rect (%d) != b.Rect (%d)", a.Rect, b.Rect)
		return
	}

	sk := getsk(a)

	yDiff := 0

	for y := 0; y < a.Rect.Dy(); y++ {
		by := a.YStride * y
		for x := 0; x < a.Rect.Dx(); x++ {
			dy := a.Y[by+x] - b.Y[by+x]
			if dy < 0 {
				dy = -dy
			}
			if dy < yDiffMinThreshold {
				// nothing
			} else {
				// TODO: maybe a sigmoid function or some piecewise linear similar thing?
				yDiff += int(dy)
			}
		}
	}
	score = float64(yDiff) / float64(255*a.Rect.Dx()*a.Rect.Dy())
	cheight := a.Rect.Dy() / sk.dy
	cwidth := a.Rect.Dx() / sk.dx
	cscore := float64(0)
	for y := 0; y < cheight; y++ {
		by := a.CStride * y
		for x := 0; x < cwidth; x++ {
			ar, ath := polarize(a.Cb[by+x], a.Cr[by+x])
			br, bth := polarize(b.Cb[by+x], b.Cr[by+x])
			dth := math.Abs(ath - bth)
			for dth > (math.Pi * 2) {
				dth -= math.Pi
			}
			cscore += dth / math.Pi
			dr := math.Abs(ar - br)
			cscore += dr / 255.0
		}
	}
	score += cscore / float64(cheight*cwidth)
	err = fmt.Errorf("TODO WRITEME")
	return
}
