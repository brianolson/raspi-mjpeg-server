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
	"net/http"
	"os"
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

// TODO: reunify with jd/getsk()
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
	th = math.Atan2(float64(y), float64(x))   // th in [-Pi, Pi]
	r = math.Sqrt(float64((x * x) + (y * y))) // r in [0, 362.0]
	return
}

var maxPolarR float64

func init() {
	maxPolarR, _ = polarize(255, 255)
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

type AHist struct {
	Ceils  []float64 `json:"c"`
	Counts []int     `json:"n"`
}

func (ah *AHist) autoHistogram(data []float64) {
	ah.Ceils, ah.Counts = autoHistogram(data)
}

type DiffScorePixHist struct {
	YPHist AHist `json:"yh"`
	CPHist AHist `json:"ch"`
}

var pixHistOut chan *DiffScorePixHist

// TODO: grab whole frame stats, histogram of per-pixel score, how many pixels were at least $x score?
func diffScoreYCbCr(a, b *image.YCbCr) (score float64, err error) {
	err = checkYCbCr(a, b)
	if err != nil {
		return
	}

	sk, err := getsk(a)
	if err != nil {
		return
	}
	cheight := a.Rect.Dy() / sk.dy
	cwidth := a.Rect.Dx() / sk.dx

	var yscores []float64
	var cscores []float64
	doPixHist := pixHistOut != nil
	if doPixHist {
		yscores = make([]float64, a.YStride*a.Rect.Dy())
		cscores = make([]float64, a.CStride*cheight)
	}

	yDiff := 0

	height := a.Rect.Dy()
	width := a.Rect.Dx()
	for y := 0; y < height; y++ {
		by := a.YStride * y
		for x := 0; x < width; x++ {
			dy := int(a.Y[by+x]) - int(b.Y[by+x])
			if dy < 0 {
				dy = -dy
			}
			if dy < yDiffMinThreshold {
				// nothing
			} else {
				// TODO: maybe a sigmoid function or some piecewise linear similar thing?
				yDiff += dy
			}
			if doPixHist {
				yscores[by+x] = float64(dy)
			}
		}
	}
	yscore := float64(yDiff) / float64(255*a.Rect.Dx()*a.Rect.Dy())
	cscore := float64(0)
	for y := 0; y < cheight; y++ {
		by := a.CStride * y
		for x := 0; x < cwidth; x++ {
			ar, ath := polarize(a.Cb[by+x], a.Cr[by+x])
			br, bth := polarize(b.Cb[by+x], b.Cr[by+x])
			dth := math.Abs(ath - bth) // dth in [0, 2*Pi]
			cs := dth / (2 * math.Pi)
			dr := math.Abs(ar - br)
			cs += dr / maxPolarR
			if doPixHist {
				cscores[by+x] = cs
			}
			cscore += cs
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
	if doPixHist {
		ph := new(DiffScorePixHist)
		ph.YPHist.autoHistogram(yscores)
		ph.CPHist.autoHistogram(cscores)
		select {
		case pixHistOut <- ph:
			// yay, send data
		default:
			// don't block, drop
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
	scoreThreshold := js.cfg.MotionScoreThreshold
	if js.cfg.MotionScoreDeactivationThreshold != 0 && js.cfg.MotionScoreDeactivationThreshold < scoreThreshold {
		scoreThreshold = js.cfg.MotionScoreDeactivationThreshold
	}
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
			if score > scoreThreshold {
				log.Printf("diff %s-%s: ds=%f", old.when, newest.when, score)
				js.motionPing(score)
			}
		}
		js.fetchHistograms()

		prev = newest
	}
}

func (js *jpegServer) fetchHistograms() {
	if pixHistOut == nil {
		return
	}
	for {
		select {
		case ph := <-pixHistOut:
			js.histogramTail.Add(ph)
			js.phLock.Lock()
			for _, subscriber := range js.pixHistSubscribers {
				select {
				case subscriber <- ph:
				default:
				}
			}
			js.phLock.Unlock()
		default:
			return
		}
	}
}
func (js *jpegServer) pixHistSubscribe(subscriber chan *DiffScorePixHist) {
	js.phLock.Lock()
	defer js.phLock.Unlock()
	for _, si := range js.pixHistSubscribers {
		if si == subscriber {
			return
		}
	}
	js.pixHistSubscribers = append(js.pixHistSubscribers, subscriber)
}
func (js *jpegServer) pixHistUnsuscribe(subscriber chan *DiffScorePixHist) {
	js.phLock.Lock()
	defer js.phLock.Unlock()
	for i, si := range js.pixHistSubscribers {
		if si == subscriber {
			last := len(js.pixHistSubscribers) - 1
			if i != last {
				js.pixHistSubscribers[i] = js.pixHistSubscribers[last]
			}
			js.pixHistSubscribers = js.pixHistSubscribers[:last]
			return
		}
	}
}

type captureThread struct {
	// file capture
	out io.WriteCloser

	js *jpegServer

	l sync.Mutex

	lastPing time.Time

	phchan  chan *DiffScorePixHist
	pixHist []any
}

func (ct *captureThread) addPixHist(ph *DiffScorePixHist) {
	select {
	case ct.phchan <- ph:
		// okay, sent
	default:
		// don't block, drop
	}
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
	motionPostDuration := time.Duration(float64(time.Second) * ct.js.cfg.MotionPostSeconds)
	var start time.Time
	var current *jpegt
	if ct.js.cfg.MotionPrerollSeconds > 0 {
		start = newest.when.Add(time.Duration(-1 * ct.js.cfg.MotionPrerollSeconds * float64(time.Second)))
		current = ct.js.getAfter(start)
		if current == nil {
			return
		}
	} else {
		start = newest.when
		current = newest
	}
	outChans := make([]chan []byte, 0, 10)
	if ct.js.cfg.MJPEGCapturePathTemplate != "" {
		fileChan := make(chan []byte, 1)
		outChans = append(outChans, fileChan)
		go ct.file(fileChan, start)
		defer close(fileChan)
	}
	if ct.js.cfg.MJPEGPostUrl != "" {
		postChan := make(chan []byte, 1)
		outChans = append(outChans, postChan)
		go ct.post(postChan)
		defer close(postChan)
	}
	// TODO: spawn MJPEGCommand here
	if len(outChans) == 0 {
		return
	}
	defer func() {
		if current != nil {
			log.Printf("recorded %s - %s", start.Format(timestampFormat), current.when.Format(timestampFormat))
		}
	}()
	for {
		for _, pc := range outChans {
			select {
			case pc <- current.blob:
			default:
			}
		}
		for ct.phchan != nil {
			select {
			case v, ok := <-ct.phchan:
				if !ok {
					ct.phchan = nil
					goto phdone
				}
				ct.pixHist = append(ct.pixHist, v)
			default:
				goto phdone
			}
		}
	phdone:

		// get next frame
		current = ct.js.waitAfter(current.when)
		if current == nil {
			log.Printf("nil current")
			return
		}
		ct.l.Lock()
		lp := ct.lastPing
		ct.l.Unlock()
		// TODO: if a frame goes above threshold in less than (MotionPrerollSeconds+MotionPostSeconds) make one long capture; don't _actually_ close a capture until after that duration of below-threshold frames. This would make MotionGapDuration obsolete?
		if current.when.After(lp.Add(motionPostDuration)) {
			return
		}
	}
}

func (ct *captureThread) post(frames chan []byte) {
	preader, pwriter := io.Pipe()
	defer pwriter.Close()
	// TODO: do some context thing so that we stop trying to send if there's an HTTP error?
	go http.Post(ct.js.cfg.MJPEGPostUrl, "video/mjpeg", preader)
	for blob := range frames {
		_, err := pwriter.Write(blob)
		if err != nil {
			log.Printf("%s: %v", ct.js.cfg.MJPEGPostUrl, err)
			return
		}
	}
}

func (ct *captureThread) file(frames chan []byte, start time.Time) {
	path := formatTemplateString(ct.js.cfg.MJPEGCapturePathTemplate, start)
	out, err := os.Create(path)
	if err != nil {
		log.Printf("%s: %v", path, err)
		return
	}
	//log.Printf("%s: capture started", path) //verbose or debug level
	defer out.Close()
	for blob := range frames {
		_, err := out.Write(blob)
		if err != nil {
			log.Printf("%s: %v", path, err)
			return
		}
	}
}
