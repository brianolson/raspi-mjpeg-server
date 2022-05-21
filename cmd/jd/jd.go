// jd demo jpeg decimate development tool
package main

import (
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"os"

	xjd "github.com/brianolson/raspi-mjpeg-server/jd"
)

func maybefail(err error, xf string, args ...interface{}) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, xf+"\n", args...)
	os.Exit(1)
}

var verbose = true

func debug(xf string, args ...interface{}) {
	if !verbose {
		return
	}
	log.Printf(xf+"\n", args...)
}

func main() {
	var fname string
	flag.StringVar(&fname, "i", "a.jpeg", "jpeg file name to read")
	var oname string
	flag.StringVar(&oname, "o", "a_sm.jpeg", "jpeg file name to write")

	flag.Parse()

	fin, err := os.Open(fname)
	maybefail(err, "%s: %v", fname, err)

	im, imfmt, err := image.Decode(fin)
	debug("imfmt: %s, %T", imfmt, im)

	imycbcr, ok := im.(*image.YCbCr)
	if ok {
		imsm, err := xjd.DecimateYCbCr(imycbcr)
		maybefail(err, "%s: decimateYCbCr %v", fname, err)
		//debug("imsm %s", &imsm)
		if oname != "" {
			fout, err := os.Create(oname)
			maybefail(err, "%s: create %v", oname, err)
			err = jpeg.Encode(fout, &imsm, &jpeg.Options{Quality: 90})
			maybefail(err, "%s: jpeg %v", oname, err)
			fout.Close()
		}
	}
}
