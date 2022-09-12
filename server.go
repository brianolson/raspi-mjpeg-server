// HTTP server stuff
// return individual jpeg or mime/multipart stream of them that is http MJPEG

package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"
)

type jpegt struct {
	// Raw JPEG bytes from source
	blob []byte
	// Time when we got this frame
	when time.Time

	// Go Image of unpacked JPEG
	unpacked image.Image
	// Tiny Image for pixel motion comparison
	mini image.Image

	// doubly linked list
	next *jpegt
	prev *jpegt
}

func (jt *jpegt) clear() {
	jt.unpacked = nil
	jt.mini = nil
}

// produce a JPEG blob from .mini
func (jt *jpegt) miniJpegBlob() (blob []byte, err error) {
	err = ensureSmall(jt, smallTargetSize)
	if err != nil {
		return
	}
	var bout bytes.Buffer
	err = jpeg.Encode(&bout, jt.mini, &jpeg.Options{Quality: 90})
	if err != nil {
		return
	}
	blob = bout.Bytes()
	return
}

type jpegServer struct {
	incoming <-chan []byte
	// Lock around any operation on the list of frames we're holding .newest to .oldest
	l sync.Mutex
	// .cond.Broadcast() when a new frame arrives
	cond *sync.Cond

	// frame just arrived
	newest *jpegt
	// last frame we're still holding
	oldest *jpegt
	count  int
	tlen   int

	// free-list
	free *jpegt

	maxcount int
	maxlen   int

	capture *captureThread

	scorestat *rollingKnnHistogram

	cfg *Config

	histogramTail RollingTail

	phLock             sync.Mutex
	pixHistSubscribers []chan *DiffScorePixHist
}

func (js *jpegServer) init() {
	// init stuff
	if js.maxcount == 0 {
		js.maxcount = 10 * 10
	}
	if js.maxlen == 0 {
		js.maxlen = 20000000
	}
	js.cond = sync.NewCond(&js.l)
	js.histogramTail.Limit = 20
}

// run thread
func (js *jpegServer) reader(ctx context.Context, wg *sync.WaitGroup) {
	if wg != nil {
		defer wg.Done()
	}
	for blob := range js.incoming {
		debug("got jpeg blob %d bytes", len(blob))
		if len(blob) < 20 {
			log.Printf("weird short jpeg %s", hex.EncodeToString(blob))
			continue
		}
		if verbose {
			br := bytes.NewReader(blob)
			_, err := jpeg.Decode(br)
			if err != nil {
				debug("[%d]byte jpeg decode error: %v", len(blob), err)
				continue
			}
		}
		js.push(blob)
		select {
		case <-ctx.Done():
			break
		default:
		}
	}
}

// receive a new jpeg blob
// keep it for later; notify listeners
func (js *jpegServer) push(blob []byte) {
	now := time.Now()
	js.l.Lock()
	defer js.l.Unlock()

	rec := js.free
	if rec == nil {
		rec = new(jpegt)
	} else {
		js.free = rec.next
		rec.clear()
	}
	rec.blob = blob
	rec.when = now
	rec.next = nil
	rec.prev = js.newest
	if js.newest != nil {
		js.newest.next = rec
	}
	js.newest = rec
	if js.oldest == nil {
		js.oldest = rec
	}
	js.count++
	js.tlen += len(blob)

	for (js.count > js.maxcount) || (js.tlen > js.maxlen) {
		if js.oldest == js.newest {
			// keep at least 1
			break
		}
		xrec := js.oldest
		js.oldest = xrec.next
		js.oldest.prev = nil
		xrec.next = js.free
		js.tlen -= len(xrec.blob)
		js.count--
		xrec.blob = nil
		xrec.prev = nil
		js.free = xrec
	}

	js.cond.Broadcast()
}

// called by motionThread when threshold exceeded
func (js *jpegServer) motionPing(score float64) {
	js.l.Lock()
	defer js.l.Unlock()

	if score > js.cfg.MotionScoreThreshold {
		// high threshold met, start or continue capture
		if js.capture == nil {
			// start capture
			if js.cfg.anyCapture() {
				js.capture = new(captureThread)
				js.capture.js = js
				js.capture.lastPing = js.newest.when
				js.capture.pixHist = js.histogramTail.ToArray()
				js.capture.phchan = make(chan *DiffScorePixHist, 10)
				js.pixHistSubscribe(js.capture.phchan)
				go js.capture.run()
			}
		} else {
			// continue capture
			js.capture.ping()
		}
	} else if js.cfg.MotionScoreDeactivationThreshold > 0 {
		if js.capture != nil && score > js.cfg.MotionScoreDeactivationThreshold {
			// lesser threshold met, continue
			js.capture.ping()
		}
	}
}

func (js *jpegServer) captureEnded() {
	js.l.Lock()
	defer js.l.Unlock()
	js.capture = nil
}

// return newest jpeg blob, handy for serving
func (js *jpegServer) getNewestJpegBlob() []byte {
	js.l.Lock()
	defer js.l.Unlock()
	if js.newest == nil {
		return nil
	}
	return js.newest.blob
}

// return newest jpeg blob and time
func (js *jpegServer) getNewest() *jpegt {
	js.l.Lock()
	defer js.l.Unlock()
	if js.newest == nil {
		return nil
	}
	return js.newest
}

// return jpeg blob and time for next frame after some time
func (js *jpegServer) getAfter(then time.Time) *jpegt {
	js.l.Lock()
	defer js.l.Unlock()
	cur := js.oldest
	for cur != nil {
		if cur.when.After(then) {
			return cur
		}
		cur = cur.next
	}
	return nil
}

// return jpeg blob and time for next frame before some time
func (js *jpegServer) getBefore(then time.Time) *jpegt {
	js.l.Lock()
	defer js.l.Unlock()
	cur := js.newest
	for cur != nil {
		if cur.when.Before(then) {
			return cur
		}
		cur = cur.prev
	}
	return nil
}

// return jpeg blob and time for next frame after some time, possibly waiting until the first frame to arrive afer that time
func (js *jpegServer) waitAfter(then time.Time) *jpegt {
	js.l.Lock()
	defer js.l.Unlock()
	cur := js.oldest
	for cur != nil {
		if cur.when.After(then) {
			return cur
		}
		cur = cur.next
	}
	for {
		js.cond.Wait()
		if js.newest != nil && js.newest.when.After(then) {
			return js.newest
		}
	}
}

var formBoolTrue = []string{"t", "1", "true"}
var formBoolFalse = []string{"f", "0", "false"}

func formBool(request *http.Request, name string, defaultValue bool) bool {
	sv := request.FormValue(name)
	if sv == "" {
		return defaultValue
	}
	sv = strings.ToLower(sv)
	for _, fv := range formBoolFalse {
		if fv == sv {
			return false
		}
	}
	for _, fv := range formBoolTrue {
		if fv == sv {
			return true
		}
	}
	return defaultValue
}

// parse an int from http form value, default if none or clamped to [min,max]
func formInt(request *http.Request, name string, defaultValue, min, max int) int {
	sv := request.FormValue(name)
	if sv == "" {
		return defaultValue
	}
	v, err := strconv.ParseInt(sv, 0, 64)
	if err != nil {
		return defaultValue
	}
	i := int(v)
	if i < min {
		return min
	}
	if i > max {
		return max
	}
	return i
}

func getOrigBlob(jt *jpegt) ([]byte, error) {
	return jt.blob, nil
}
func getMiniBlob(jt *jpegt) ([]byte, error) {
	return jt.miniJpegBlob()
}

func httpErr(err error, w http.ResponseWriter, code int, xf string, args ...interface{}) bool {
	if err == nil {
		return false
	}
	msg := fmt.Sprintf(xf, args...)
	http.Error(w, msg, code)
	return true
}

//
//
// serves:
// /jpeg most recent
// /favicon.ico
// / -redirect> /mjpeg
// /* mjpeg stream, ?fps=<frames per second 1..30 (15)>&start=<seconds ago, -100..0 (0)>
func (js *jpegServer) ServeHTTP(out http.ResponseWriter, request *http.Request) {
	path := request.URL.Path
	if path == "/jpeg" {
		out.Header().Set("Content-Type", "image/jpeg")
		out.Write(js.getNewestJpegBlob())
		return
	}
	if path == "/favicon.ico" {
		http.Error(out, "no", http.StatusNotFound)
		return
	}
	if path == "/" {
		http.Redirect(out, request, "/mjpeg", http.StatusFound)
		return
	}
	if path == "/debug" {
		js.debugStream(out, request)
		return
	}

	// Serve MJPEG...
	js.frameStream(out, request)
}
func (js *jpegServer) ServeHTTPX(out http.ResponseWriter, request *http.Request) {
	request.ParseForm()
	fps := formInt(request, "fps", 15, 1, 30)
	offset := formInt(request, "start", 0, -100, 0)
	showMini := formBool(request, "mini", false)
	var getBlobF func(*jpegt) ([]byte, error)
	if showMini {
		getBlobF = getMiniBlob
	} else {
		getBlobF = getOrigBlob
	}

	period := time.Second / time.Duration(fps)
	startTime := time.Now()
	nextFrame := startTime.Add(period)
	caughtUp := true
	var blob []byte
	var when time.Time
	var err error

	if offset < 0 {
		startTime = startTime.Add(time.Duration(offset) * time.Second)
		thenjt := js.getAfter(startTime)
		blob, err = getBlobF(thenjt)
		if httpErr(err, out, 500, "getAfter blob %v", err) {
			return
		}
		when = thenjt.when
		caughtUp = (blob == nil)
	}

	m := multipart.NewWriter(out)
	defer m.Close()

	out.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+m.Boundary())
	out.Header().Set("Connection", "close")
	h := textproto.MIMEHeader{}
	st := fmt.Sprint(startTime.Unix())
	for {
		if blob == nil {
			newest := js.getNewest()
			blob, err = getBlobF(newest)
			if httpErr(err, out, 500, "getNewest blob %v", err) {
				return
			}
			when = newest.when
		} else {
			if !caughtUp {
				next := js.getAfter(when)
				if next == nil {
					caughtUp = true
				} else {
					blob, err = getBlobF(next)
					if httpErr(err, out, 500, "getAfter blob2 %v", err) {
						return
					}
					when = next.when
				}
			}
			if caughtUp {
				newest := js.waitAfter(nextFrame)
				blob, err = getBlobF(newest)
				if httpErr(err, out, 500, "waitAfter blob %v", err) {
					return
				}
				when = newest.when
				nextFrame = when
			}
		}
		if blob == nil {
			// TODO: log error?
			break
		}
		h.Set("Content-Type", "image/jpeg")
		h.Set("Content-Length", fmt.Sprint(len(blob)))
		h.Set("X-StartTime", st)
		h.Set("X-TimeStamp", fmt.Sprint(when.Unix()))
		mw, err := m.CreatePart(h)
		if err != nil {
			break
		}
		_, err = mw.Write(blob)
		if err != nil {
			break
		}
		if flusher, ok := mw.(http.Flusher); ok {
			flusher.Flush()
		}

		now := time.Now()
		if now.Before(nextFrame) {
			time.Sleep(nextFrame.Sub(now))
		}
		nextFrame = nextFrame.Add(period)
	}
}

func (js *jpegServer) frameStream(out http.ResponseWriter, request *http.Request) {
	request.ParseForm()
	fps := formInt(request, "fps", 15, 1, 30)
	offset := formInt(request, "start", 0, -100, 0)
	showMini := formBool(request, "mini", false)
	var getBlobF func(*jpegt) ([]byte, error)
	if showMini {
		getBlobF = getMiniBlob
	} else {
		getBlobF = getOrigBlob
	}
	jblobs := make(chan []byte, 0)
	ctx, cf := context.WithCancel(request.Context())
	done := ctx.Done()
	go js.serveMJPEG(out, request, jblobs, ctx, cf)

	period := time.Second / time.Duration(fps)
	startTime := time.Now()
	nextFrame := startTime.Add(period)
	caughtUp := true
	var blob []byte
	var when time.Time
	var err error

	defer close(jblobs)

	if offset < 0 {
		startTime = startTime.Add(time.Duration(offset) * time.Second)
		thenjt := js.getAfter(startTime)
		blob, err = getBlobF(thenjt)
		if httpErr(err, out, 500, "getAfter blob %v", err) {
			return
		}
		when = thenjt.when
		caughtUp = (blob == nil)
	}
	for {
		if blob == nil {
			newest := js.getNewest()
			blob, err = getBlobF(newest)
			if httpErr(err, out, 500, "getNewest blob %v", err) {
				return
			}
			when = newest.when
		} else {
			if !caughtUp {
				next := js.getAfter(when)
				if next == nil {
					caughtUp = true
				} else {
					blob, err = getBlobF(next)
					if httpErr(err, out, 500, "getAfter blob2 %v", err) {
						return
					}
					when = next.when
				}
			}
			if caughtUp {
				newest := js.waitAfter(nextFrame)
				blob, err = getBlobF(newest)
				if httpErr(err, out, 500, "waitAfter blob %v", err) {
					return
				}
				when = newest.when
				nextFrame = when
			}
		}
		if blob == nil {
			return
		}

		select {
		case jblobs <- blob:
			// ok
		case <-done:
			return
		}

		now := time.Now()
		if now.Before(nextFrame) {
			time.Sleep(nextFrame.Sub(now))
		}
		nextFrame = nextFrame.Add(period)
	}
}

func fs(then time.Time) string {
	return fmt.Sprintf("%0.3f", float64(then.UnixMilli())/1000.0)
}

func (js *jpegServer) serveMJPEG(out http.ResponseWriter, request *http.Request, jblobs chan []byte, ctx context.Context, cf func()) {
	var blob []byte
	m := multipart.NewWriter(out)
	defer cf()
	defer m.Close()

	out.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+m.Boundary())
	out.Header().Set("Connection", "close")
	h := textproto.MIMEHeader{}
	st := fs(time.Now())
	for {
		select {
		case blob = <-jblobs:
		case <-ctx.Done():
			break
		}
		if blob == nil {
			// TODO: log error?
			break
		}
		h.Set("Content-Type", "image/jpeg")
		h.Set("Content-Length", fmt.Sprint(len(blob)))
		h.Set("X-StartTime", st)
		h.Set("X-TimeStamp", fs(time.Now()))
		mw, err := m.CreatePart(h)
		if err != nil {
			break
		}
		_, err = mw.Write(blob)
		if err != nil {
			break
		}
		if flusher, ok := mw.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func (js *jpegServer) debugStream(out http.ResponseWriter, request *http.Request) {
	request.ParseForm()
	fps := formInt(request, "fps", 15, 1, 30)
	thresh := formInt(request, "thresh", yDiffMinThreshold, 0, 255)
	diffMode := formBool(request, "d", false)
	period := time.Second / time.Duration(fps)
	jblobs := make(chan []byte, 0)
	ctx, cf := context.WithCancel(request.Context())
	done := ctx.Done()
	go js.serveMJPEG(out, request, jblobs, ctx, cf)

	startTime := time.Now()
	nextFrame := startTime

	defer close(jblobs)

	for {
		newest := js.waitAfter(nextFrame)
		if newest == nil {
			return
		}
		nextFrame = newest.when
		var blob []byte
		if diffMode {
			old := js.getAfter(newest.when.Add(-1 * time.Second))
			if old == nil {
				select {
				case <-done:
					return
				default:
				}
				log.Print("no old frame yet")
				continue
			} else {
				var err error
				blob, err = debugImDiff(old, newest, uint8(thresh))
				if err != nil {
					log.Printf("debugImDiff: %v", err)
					continue
				}
			}
		} else {
			blob = newest.blob
		}

		select {
		case jblobs <- blob:
			// ok
		case <-done:
			return
		}

		now := time.Now()
		if now.Before(nextFrame) {
			time.Sleep(nextFrame.Sub(now))
		}
		nextFrame = nextFrame.Add(period)
	}
}

func debugImDiff(old, new *jpegt, thresh uint8) (blob []byte, err error) {
	err = ensureSmall(old, smallTargetSize)
	if err != nil {
		return
	}
	err = ensureSmall(new, smallTargetSize)
	if err != nil {
		return
	}
	aycbcr, ok := old.mini.(*image.YCbCr)
	if !ok {
		err = fmt.Errorf("TODO: WRITEME debugImDiff %T", old.mini)
		return
	}
	bycbcr, ok := new.mini.(*image.YCbCr)
	if !ok {
		err = fmt.Errorf("TODO: WRITEME debugImDiff %T", new.mini)
		return
	}
	return debugImDiffYCbCr(aycbcr, bycbcr, thresh)
}

func debugImDiffYCbCr(a, b *image.YCbCr, thresh uint8) (blob []byte, err error) {
	err = checkYCbCr(a, b)
	if err != nil {
		return
	}

	//out := image.NewYCbCr(a.Rect, a.SubsampleRatio)
	out := image.NewGray(a.Rect)
	for y := 0; y < a.Rect.Dy(); y++ {
		by := a.YStride * y
		for x := 0; x < a.Rect.Dx(); x++ {
			dy := int(a.Y[by+x]) - int(b.Y[by+x])
			ady := dy
			if ady < 0 {
				ady = -ady
			}
			if ady < int(thresh) {
				// flat grey
				//out.Y[by+x] = 0x7f
				out.Pix[(y*out.Stride)+x] = 0x7f
			} else {
				//out.Y[by+x] = 0x7f + (dy / 2)
				out.Pix[(y*out.Stride)+x] = uint8(0x7f + (dy / 2))
			}
		}
	}
	var blobw bytes.Buffer
	err = jpeg.Encode(&blobw, out, &jpeg.Options{Quality: 90})
	blob = blobw.Bytes()
	return
}
