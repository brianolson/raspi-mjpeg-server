// HTTP server stuff
// return individual jpeg or mime/multipart stream of them that is http MJPEG

package main

import (
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"sync"
	"time"
)

type jpegt struct {
	blob []byte
	when time.Time

	next *jpegt
	prev *jpegt
}

type jpegServer struct {
	incoming <-chan []byte
	l        sync.Mutex
	cond     *sync.Cond

	newest *jpegt
	oldest *jpegt
	count  int
	tlen   int

	free *jpegt

	maxcount int
	maxlen   int
}

// run thread
func (js *jpegServer) reader(wg *sync.WaitGroup) {
	// init stuff
	if js.maxcount == 0 {
		js.maxcount = 10 * 10
	}
	if js.maxlen == 0 {
		js.maxlen = 20000000
	}
	js.cond = sync.NewCond(&js.l)

	if wg != nil {
		defer wg.Done()
	}
	for blob := range js.incoming {
		js.push(blob)
	}
}

func (js *jpegServer) push(blob []byte) {
	now := time.Now()
	js.l.Lock()
	defer js.l.Unlock()

	rec := js.free
	if rec == nil {
		rec = new(jpegt)
	} else {
		js.free = rec.next
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

func (js *jpegServer) getNewest() []byte {
	js.l.Lock()
	defer js.l.Unlock()
	if js.newest == nil {
		return nil
	}
	return js.newest.blob
}

// return a copy of newest jpegt
// copy works around some threading mess
func (js *jpegServer) getNewestJT() (blob []byte, when time.Time) {
	js.l.Lock()
	defer js.l.Unlock()
	if js.newest == nil {
		blob = nil
		return
	}
	blob = js.newest.blob
	when = js.newest.when
	return
}
func (js *jpegServer) getAfterJT(then time.Time) (blob []byte, when time.Time) {
	js.l.Lock()
	defer js.l.Unlock()
	cur := js.oldest
	for cur != nil {
		if cur.when.After(then) {
			blob = cur.blob
			when = cur.when
			return
		}
		cur = cur.next
	}
	blob = nil
	return
}
func (js *jpegServer) waitAfterJT(then time.Time) (blob []byte, when time.Time) {
	js.l.Lock()
	defer js.l.Unlock()
	cur := js.oldest
	for cur != nil {
		if cur.when.After(then) {
			blob = cur.blob
			when = cur.when
			return
		}
		cur = cur.next
	}
	for {
		js.cond.Wait()
		if js.newest != nil && js.newest.when.After(then) {
			blob = js.newest.blob
			when = js.newest.when
			return
		}
	}
}

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

func (js *jpegServer) ServeHTTP(out http.ResponseWriter, request *http.Request) {
	path := request.URL.Path
	if path == "/jpeg" {
		out.Header().Set("Content-Type", "image/jpeg")
		out.Write(js.getNewest())
		return
	}
	if path == "/" {
		http.Redirect(out, request, "/mjpeg", http.StatusFound)
		return
	}

	request.ParseForm()
	fps := formInt(request, "fps", 15, 1, 30)
	offset := formInt(request, "start", 0, -100, 0)

	period := time.Second / time.Duration(fps)
	startTime := time.Now()
	nextFrame := startTime.Add(period)
	caughtUp := true
	var blob []byte
	var when time.Time

	if offset < 0 {
		startTime = startTime.Add(time.Duration(offset) * time.Second)
		blob, when = js.getAfterJT(startTime)
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
			blob, when = js.getNewestJT()
		} else {
			if !caughtUp {
				nb, nw := js.getAfterJT(when)
				if nb == nil {
					caughtUp = true
				} else {
					blob = nb
					when = nw
				}
			}
			if caughtUp {
				blob, when = js.waitAfterJT(nextFrame)
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
