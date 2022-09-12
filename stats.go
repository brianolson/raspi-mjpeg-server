package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
)

type rollingKnnHistogram struct {
	name       string
	buffer     []float64
	altBuffer  []float64
	pos        int
	out        io.Writer
	knnBuckets int
	knnRounds  int

	logRaw bool

	min float64
	max float64
}

func NewRollingKnnHistogram(name string, size int, fout io.Writer) *rollingKnnHistogram {
	out := new(rollingKnnHistogram)
	out.buffer = make([]float64, size)
	out.out = fout
	out.name = name
	return out
}

func NewRollingKnnHistogramPOST(name string, size int, url string) *rollingKnnHistogram {
	out := new(rollingKnnHistogram)
	out.buffer = make([]float64, size)
	out.name = name
	out.out = &statSender{url: url}
	return out
}

// Add a record.
// May spawn a thread to digest data and log it.
func (rkh *rollingKnnHistogram) Add(x float64) {
	if rkh.pos == 0 {
		rkh.min = x
		rkh.max = x
	} else {
		if x < rkh.min {
			rkh.min = x
		}
		if x > rkh.max {
			rkh.max = x
		}
	}
	rkh.buffer[rkh.pos] = x
	rkh.pos++
	if rkh.pos == len(rkh.buffer) {
		if rkh.altBuffer == nil {
			rkh.altBuffer = make([]float64, len(rkh.buffer))
		}
		t := rkh.buffer
		rkh.buffer = rkh.altBuffer
		rkh.altBuffer = t
		rkh.pos = 0
		go rkh.logKnnStats(t, rkh.max, rkh.min)
	}
}

// Flush writes as much as we have to the log file immediately.
func (rkh *rollingKnnHistogram) Flush() {
	if rkh.altBuffer == nil {
		rkh.altBuffer = make([]float64, len(rkh.buffer))
	}
	tl := rkh.buffer[:rkh.pos]
	t := rkh.buffer
	rkh.buffer = rkh.altBuffer
	rkh.altBuffer = t
	rkh.pos = 0
	rkh.logKnnStats(tl, rkh.max, rkh.min)
}

type KnnStatLogEntry struct {
	Centers []float64 `json:"kc"`
	Counts  []int     `json:"c"`
	Raw     []float64 `json:"d,omitempty"`
}

func (rkh *rollingKnnHistogram) logKnnStats(buffer []float64, max, min float64) {
	if rkh.knnBuckets == 0 {
		rkh.knnBuckets = 20
	}
	if rkh.knnRounds == 0 {
		rkh.knnRounds = 20
	}

	centers := make([]float64, rkh.knnBuckets)
	sums := make([]float64, rkh.knnBuckets)
	counts := make([]int, rkh.knnBuckets)
	span := max - min
	step := span / float64(rkh.knnBuckets)
	for i := 0; i < rkh.knnBuckets; i++ {
		centers[i] = min + (step * float64(i))
	}

	for r := 0; r < rkh.knnRounds; r++ {
		knnCount(centers, sums, counts, buffer)
		knnAdjust(centers, sums, counts)
	}
	centers, counts = filterZeroCounts(centers, counts)
	rec := KnnStatLogEntry{Centers: centers, Counts: counts}
	if rkh.logRaw {
		rec.Raw = buffer
	}
	var blob []byte
	var err error
	if rkh.name != "" {
		xrec := make(map[string]interface{}, 1)
		xrec[rkh.name] = rec
		blob, err = json.Marshal(xrec)
	} else {
		blob, err = json.Marshal(rec)
	}
	if err != nil {
		log.Printf("json.Marshal(KnnStatLogEntry) %v", err)
		return
	}
	blob = append(blob, '\n')
	_, err = rkh.out.Write(blob)
	if err != nil {
		log.Printf("logKnnStats Write %v", err)
		return
	}
	log.Printf("statlog %d recs knn", len(buffer))
}

func knnCount(centers, sums []float64, counts []int, buffer []float64) {
	for i := 0; i < len(sums); i++ {
		sums[i] = 0
		counts[i] = 0
	}
	sort.Float64s(centers)
	for _, v := range buffer {
		if math.IsNaN(v) {
			continue
		}
		lo := 0
		hi := len(centers) - 1
		for {
			if hi == lo+1 {
				break
			}
			mid := (hi + lo) / 2
			if mid == hi {
				mid--
			}
			if mid == lo {
				mid++
			}
			// find nearest
			if centers[mid] > v {
				hi = mid
			} else if centers[mid] < v {
				lo = mid
			} else {
				// mid == v
				lo = mid
				hi = mid
				break
			}
		}
		dl := math.Abs(centers[lo] - v)
		dh := math.Abs(centers[hi] - v)
		if dl < dh {
			// lo is closest
			counts[lo]++
			sums[lo] += v
		} else {
			// hi is closest
			counts[hi]++
			sums[hi] += v
		}
	}
}

func knnAdjust(centers, sums []float64, counts []int) {
	// move centers to the center they actually were
	for i := range centers {
		if counts[i] != 0 {
			centers[i] = sums[i] / float64(counts[i])
		}
	}
}

func filterZeroCounts(centers []float64, counts []int) ([]float64, []int) {
	i := 0
	for i < len(counts) {
		if counts[i] == 0 {
			for j := i + 1; j < len(counts); j++ {
				counts[j-1] = counts[j]
				centers[j-1] = centers[j]
			}
			counts = counts[:len(counts)-1]
			centers = centers[:len(centers)-1]
		} else {
			i++
		}
	}
	return centers, counts
}

type statSender struct {
	url string
}

// implement io.Writer
// but we know it always gets a whole message from .logKnnStats()
func (sender *statSender) Write(blob []byte) (int, error) {
	br := bytes.NewReader(blob)
	response, err := http.Post(sender.url, "application/json", br)
	if err != nil {
		return 0, err
	}
	if response.StatusCode == 200 {
		return len(blob), nil
	}
	return 0, fmt.Errorf("status %s", response.Status)
}

// count[i] is number of points <= ceils[i] (and > ceils[i-1])
func autoHistogram(data []float64) (ceils []float64, counts []int) {
	const buckets = 30
	sum := float64(0)
	min := data[0]
	max := data[0]
	for _, v := range data {
		sum += v
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	mean := sum / float64(len(data))
	variance := float64(0)
	for _, v := range data {
		d := v - mean
		variance += (d * d)
	}
	variance /= float64(len(data))
	pstddev := math.Sqrt(variance)
	counts = make([]int, buckets+1)
	ceils = make([]float64, buckets+1)
	lo := mean - (3 * pstddev)
	if lo < min {
		lo = min
	}
	hi := mean + (3 * pstddev)
	if hi > max {
		hi = max
	}
	span := hi - lo
	for i := 0; i < buckets; i++ {
		ceils[i] = lo + ((span / 30) * float64(i))
	}
	ceils[buckets] = hi
	for _, v := range data {
		for i, ce := range ceils {
			if v <= ce {
				counts[i]++
				break
			}
		}
	}
	return ceils, counts
}
