package main

import (
	"encoding/json"
	"io"
	"log"
	"math"
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
	rec := KnnStatLogEntry{Centers: centers, Counts: counts}
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
}

func knnCount(centers, sums []float64, counts []int, buffer []float64) {
	for i := 0; i < len(sums); i++ {
		sums[i] = 0
		counts[i] = 0
	}
	sort.Float64s(centers)
	for _, v := range buffer {
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
		centers[i] = sums[i] / float64(counts[i])
	}
}
