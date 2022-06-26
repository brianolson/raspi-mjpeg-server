package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

func maybefail(err error, xf string, args ...interface{}) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, xf+"\n", args...)
	os.Exit(1)
}

var verbose = false

func debug(xf string, args ...interface{}) {
	if !verbose {
		return
	}
	log.Printf(xf+"\n", args...)
}

var defaultcmd = `{"cmd":["libcamera-vid", "-t", "60000", "-n", "--framerate", "7", "--codec", "mjpeg", "--awb", "auto", "--width", "1920", "--height", "1080", "-o", "-"], "retry":"500ms"}`

func startShutdown(server *http.Server, wg *sync.WaitGroup, ctx context.Context) {
	wg.Add(1)
	defer wg.Done()
	err := server.Shutdown(ctx)
	if err != nil {
		log.Printf("http shutdown: %v", err)
	}
}

func signalHandler(server *http.Server, wg *sync.WaitGroup, ctx context.Context) {
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sc:
		log.Printf("shutting down...")
		startShutdown(server, wg, ctx)
	case <-ctx.Done():
	}
}

var motionScoreThreshold float64 = 0.03
var motionPrerollSeconds float64 = 1.0
var motionPostSeconds float64 = 1.0
var motionPostDuration time.Duration = time.Second
var mjpegCapturePathTemplate string = ""

func main() {
	var cmd string
	flag.StringVar(&cmd, "cmd", defaultcmd, "json {\"cmd\":[], \"retry\":\"1000ms\"}, may be json literal or filename or \"-\" for stdin json")
	var addr string
	flag.StringVar(&addr, "addr", ":8412", "host:port for http serving")
	var logPath string
	flag.StringVar(&logPath, "log", "", "path to log to (stderr default)")
	flag.BoolVar(&verbose, "verbose", false, "more logging")
	var statLogPath string
	flag.StringVar(&statLogPath, "statlog", "", "path to log json-per-line stats to")
	flag.StringVar(&mjpegCapturePathTemplate, "mjpeg", "", "path to store motion mjpeg captures, %T gets timestamp")

	flag.Parse()

	debug("verbose enabled")

	if logPath != "" {
		lout, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		maybefail(err, "%s: %v", logPath, err)
		log.SetOutput(lout)
		defer lout.Close()
	} else {
		log.SetOutput(os.Stderr)
	}

	if statLogPath != "" {
		statLogF, err := os.OpenFile(statLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		maybefail(err, "%s: %v", statLogPath, err)
		defer statLogF.Close()
		statLog = statLogF
	}

	motionPostDuration = time.Duration(float64(time.Second) * motionPostSeconds)

	ctx := context.Background()

	ctx, ctxCf := context.WithCancel(ctx)
	defer ctxCf()

	var shutdownWg sync.WaitGroup

	if len(cmd) == 0 {
		fmt.Fprintf(os.Stderr, "-cmd is required")
		os.Exit(1)
	}
	var source *commandMJPEGSource
	var err error
	if cmd == "-" {
		source, err = JsonCmd(ctx, os.Stdin)
		maybefail(err, "stdin: %v", err)
	} else if cmd[0] == '{' {
		source, err = JsonCmd(ctx, strings.NewReader(cmd))
		maybefail(err, "-cmd: %v", err)
	} else {
		jsf, err := os.Open(cmd)
		maybefail(err, "%s: %v", cmd, err)
		source, err = JsonCmd(ctx, jsf)
		maybefail(err, "%s: %v", cmd, err)
	}
	source.Init()
	go source.Run()
	br := bufio.NewReader(source)
	jpegBlobs := make(chan []byte, 1)
	go func() {
		me := breakBinaryMJPEGStream(br, jpegBlobs)
		fmt.Printf("mjpeg stream err: %v\n", me)
	}()

	js := jpegServer{
		incoming: jpegBlobs,
	}
	js.init()
	go js.reader(ctx, nil)
	go js.motionThread(ctx)
	if statLog != nil {
		js.scorestat = NewRollingKnnHistogram("s", 1000, statLog)
	}

	server := &http.Server{
		Addr:    addr,
		Handler: &js,
	}
	log.Printf("serving on %s", addr)
	go signalHandler(server, &shutdownWg, ctx)
	err = server.ListenAndServe()
	if err == http.ErrServerClosed {
		// okay, wait for Shutdown
		shutdownWg.Wait()
	} else {
		maybefail(err, "http: %v", err)
	}
}
