package jd

import (
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
)

var Verbose io.Writer = nil

func debug(xf string, args ...interface{}) {
	if Verbose == nil {
		return
	}
	fmt.Fprintf(Verbose, xf+"\n", args...)
}

func iabsd(a, b int) int {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}

// find an integer divisor of rectangle size and some other ints
// target rectangle side size is `target`
// return divisor
func findDivisor(r image.Rectangle, other []int, target int) int {
	width := r.Dx()
	height := r.Dy()
	besti := 1
	bestd := target * 99
	for i := 1; i < 50; i++ {
		nw := width / i
		if (nw * i) != width {
			// not int divisor
			continue
		}
		nh := height / i
		if (nh * i) != height {
			// not int divisor
			continue
		}
		for _, x := range other {
			nx := x / i
			if (nx * i) != x {
				continue
			}
		}
		d := iabsd(nw, target) + iabsd(nh, target)
		if d < bestd {
			bestd = d
			besti = i
		} else if d > bestd {
			break
		}
	}
	return besti
}

func DecimateYCbCr(im *image.YCbCr) (out image.YCbCr, err error) {
	debug("YCbCr: YStride %d, CStride %d, sub %s, %s", im.YStride, im.CStride, im.SubsampleRatio, im.Rect)
	var other = [5]int{len(im.Y), len(im.Cb), len(im.Cr), im.YStride, im.CStride}
	div := findDivisor(im.Rect, other[:], 150)
	if im.SubsampleRatio != image.YCbCrSubsampleRatio422 {
		err = fmt.Errorf("TODO: write code for subsample %s", im.SubsampleRatio)
		return
	}
	width := im.Rect.Dx()
	height := im.Rect.Dy()
	nw := width / div
	nh := height / div
	debug("divisor %d -> (%d x %d) , %d pix", div, nw, nh, nw*nh)

	out.Y = make([]uint8, len(im.Y)/div)
	out.Cb = make([]uint8, len(im.Cb)/div)
	out.Cr = make([]uint8, len(im.Cr)/div)
	out.YStride = im.YStride / div
	out.CStride = im.CStride / div
	out.SubsampleRatio = im.SubsampleRatio
	out.Rect.Min.X = 0
	out.Rect.Min.Y = 0
	out.Rect.Max.X = nw
	out.Rect.Max.Y = nh

	rowy := make([]int, out.YStride)
	rowcb := make([]int, out.CStride)
	rowcr := make([]int, out.CStride)

	dd := div * div

	for y := 0; y < height; y++ {
		by := im.YStride * y
		for x := 0; x < width; x++ {
			rowy[x/div] += int(im.Y[by+x])
		}
		if im.SubsampleRatio == image.YCbCrSubsampleRatio422 {
			// half as many horizontal samples in Cb/Cr
			bc := im.CStride * y
			for x := 0; x < width/2; x++ {
				rowcb[x/div] += int(im.Cb[bc+x])
				rowcr[x/div] += int(im.Cr[bc+x])
			}
		} else {
			err = fmt.Errorf("TODO: write code for subsample %s", im.SubsampleRatio)
			return
		}
		if (y+1)%div == 0 {
			// commit rows and clear
			oby := out.YStride * (y / div)
			for x := 0; x < nw; x++ {
				out.Y[oby+x] = uint8(rowy[x] / dd)
				rowy[x] = 0
			}
			if im.SubsampleRatio == image.YCbCrSubsampleRatio422 {
				// half as many horizontal samples in Cb/Cr
				obc := out.CStride * (y / div)
				for x := 0; x < nw/2; x++ {
					out.Cb[obc+x] = uint8(rowcb[x] / dd)
					rowcb[x] = 0
					out.Cr[obc+x] = uint8(rowcr[x] / dd)
					rowcr[x] = 0
				}
			} else {
				err = fmt.Errorf("TODO: write code for subsample %s", im.SubsampleRatio)
				return
			}
		}
	}
	err = nil
	return
}
