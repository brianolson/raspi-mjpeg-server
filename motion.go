package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/brianolson/raspi-mjpeg-server/jd"
)

var statLog io.Writer

func ensureSmall(x *jpegt, targetSize int) (err error) {
	if x.unpacked == nil {
		br := bytes.NewReader(x.blob)
		x.unpacked, _, err = image.Decode(br)
		if err != nil {
			err = fmt.Errorf("ensureSmall could not decode blob: %v", err)
			return
		}
	}
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
	subsampleParts{image.YCbCrSubsampleRatio420, 2, 2},
	subsampleParts{image.YCbCrSubsampleRatio440, 2, 1},
	subsampleParts{image.YCbCrSubsampleRatio411, 1, 4},
	subsampleParts{image.YCbCrSubsampleRatio410, 2, 4},
}

var ErrUnknownSubsample = errors.New("unknown YCbCrSubsampleRatio")

func getsk(im *image.YCbCr) (out subsampleParts, err error) {
	for _, sk := range subsampleKey {
		if sk.rat == im.SubsampleRatio {
			out = sk
			return
		}
	}

	err = ErrUnknownSubsample
	return
}

func polarize(x, y uint8) (r, th float64) {
	th = math.Atan2(float64(y), float64(x))
	r = math.Sqrt(float64((x * x) + (y * y)))
	return
}

func checkYCbCr(a, b *image.YCbCr) (err error) {
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
	return
}

type diffScoreYCbCrStat struct {
	Stat   string  `json:"stat"` // always "dsYCbCr"
	YScore float64 `json:"ys"`
	CScore float64 `json:"cs"`
	Score  float64 `json:"ds"`
}

func diffScoreYCbCr(a, b *image.YCbCr) (score float64, err error) {
	err = checkYCbCr(a, b)
	if err != nil {
		return
	}

	sk, err := getsk(a)
	if err != nil {
		return
	}

	yDiff := 0

	for y := 0; y < a.Rect.Dy(); y++ {
		by := a.YStride * y
		for x := 0; x < a.Rect.Dx(); x++ {
			dy := int(a.Y[by+x]) - int(b.Y[by+x])
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
	yscore := float64(yDiff) / float64(255*a.Rect.Dx()*a.Rect.Dy())
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
	cscore = cscore / float64(cheight*cwidth)
	if false && statLog != nil {
		rec := diffScoreYCbCrStat{
			Stat:   "dsYCbCr",
			YScore: yscore,
			CScore: cscore,
			Score:  yscore + cscore,
		}
		blob, merr := json.Marshal(rec)
		if merr == nil {
			blob = append(blob, '\n')
			statLog.Write(blob)
		}
	}
	score = yscore + cscore
	return
}

const timestampFormat = "20060102_150405.999999999"

func (js *jpegServer) motionThread(ctx context.Context) {
	var prev *jpegt
	var old *jpegt
	var then time.Time
	for {
		select {
		case <-ctx.Done():
			break
		default:
		}
		js.l.Lock()
		js.cond.Wait()
		newest := js.newest
		old = nil
		if (newest != nil) && (newest != prev) {
			old = newest.prev
			then = newest.when.Add(-1 * time.Second) // TODO: configurable
			for old != nil {
				if old.when.Before(then) {
					break
				}
				old = old.prev
			}
		}
		js.l.Unlock()
		select {
		case <-ctx.Done():
			break
		default:
		}
		if newest == nil {
			continue
		}
		if newest == prev {
			continue
		}
		if old == nil {
			continue
		}

		score, err := smallDiff(old, newest)
		if err != nil {
			log.Printf("diff %s-%s: %v", old.when, newest.when, err)
		} else if math.IsNaN(score) {
			log.Printf("diff %s-%s: is NaN", old.when, newest.when)
		} else {
			if js.scorestat != nil {
				js.scorestat.Add(score)
			}
			if score > motionScoreThreshold {
				log.Printf("diff %s-%s: ds=%f", old.when, newest.when, score)
				js.motionPing()
			}
		}

		prev = newest
	}
}

type captureThread struct {
	out io.WriteCloser

	js *jpegServer

	l sync.Mutex

	lastPing time.Time
}

func (ct *captureThread) ping() {
	ct.l.Lock()
	defer ct.l.Unlock()
	ct.lastPing = time.Now()
}

func (ct *captureThread) run() {
	defer ct.js.captureEnded()
	newest := ct.js.getNewest()
	if newest == nil {
		return
	}
	var start time.Time
	var current *jpegt
	if motionPrerollSeconds > 0 {
		start = newest.when.Add(time.Duration(-1 * motionPrerollSeconds * float64(time.Second)))
		current = ct.js.getAfter(start)
		if current == nil {
			return
		}
	} else {
		start = newest.when
		current = newest
	}
	path := strings.ReplaceAll(mjpegCapturePathTemplate, "%T", start.Format(timestampFormat))
	var err error
	ct.out, err = os.Create(path)
	if err != nil {
		log.Printf("%s: %v", path, err)
		return
	}
	log.Printf("%s: capture started", path)
	defer ct.out.Close()
	defer func() {
		if current != nil {
			log.Printf("%s: recorded %s - %s", path, start.Format(timestampFormat), current.when.Format(timestampFormat))
		}
	}()
	_, err = ct.out.Write(current.blob)
	if err != nil {
		log.Printf("%s: %v", path, err)
		return
	}
	for {
		current = ct.js.waitAfter(current.when)
		if current == nil {
			log.Printf("%s: nil current", path)
			return
		}
		ct.l.Lock()
		lp := ct.lastPing
		ct.l.Unlock()
		if current.when.After(lp.Add(motionPostDuration)) {
			log.Printf("%s: done", path)
			// done
			return
		}
		_, err = ct.out.Write(current.blob)
		if err != nil {
			log.Printf("%s: %v", path, err)
			return
		}
	}
}
