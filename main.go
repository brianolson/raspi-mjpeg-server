package main

import (
	"context"
	"encoding/json"
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

type Config struct {
	// MotionScoreThreshold is the number above which a frame must be to start a capture or below which frames fall to end a capture
	MotionScoreThreshold float64 `json:"threshold"`

	// MotionScoreDeactivationThreshold is the threshold below which frames must fall to stop a capture. (default same as MotionScoreThreshold
	MotionScoreDeactivationThreshold float64 `json:"thresh-off"`

	// MotionPrerollSeconds allows starting the capture just before the motion got above threshold.
	MotionPrerollSeconds float64 `json:"pre-sec"`

	// MotionPostSeconds is how long to continue capture after frames fall below MotionScoreThreshold
	MotionPostSeconds float64 `json:"post-sec"`

	// MJPEGCapturePathTemplate is the path to store motion mjpeg captures, %T gets timestamp
	MJPEGCapturePathTemplate string `json:"mjpeg-path,omitempty"`

	// MJPEGCommand is a command to pipe mjpeg data to (e.g. ["ffmpeg", ...])
	// TODO: implement
	// %T gets timestamp
	MJPEGCommand []string `json:"mjpeg-cmd,omitempty"`

	// MJPEGPostUrl is a url to POST "video/mjpeg" content to
	// TODO: implement
	MJPEGPostUrl string `json:"mjpeg-url,omitempty"`
}

func (cfg Config) anyCapture() bool {
	if cfg.MJPEGCapturePathTemplate != "" {
		return true
	}
	for _, v := range cfg.MJPEGCommand {
		if v != "" {
			return true
		}
	}
	if cfg.MJPEGPostUrl != "" {
		return true
	}
	return false
}

var defaultConfig Config = Config{
	MotionScoreThreshold:     0.05,
	MotionPrerollSeconds:     1.0,
	MotionPostSeconds:        1.0,
	MJPEGCapturePathTemplate: "",
	MJPEGCommand:             nil,
}

/*
var motionScoreThreshold float64 = 0.03
var motionPrerollSeconds float64 = 1.0
var motionPostSeconds float64 = 1.0
var motionPostDuration time.Duration = time.Second
   var mjpegCapturePathTemplate string = ""
*/

func formatTemplateString(x string, when time.Time) string {
	// "%%" becomes "%"
	// e.g. "%%T" -> "%T"
	parts := strings.Split(x, "%%")
	timestamp := when.Format(timestampFormat)
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(p, "%T", timestamp)
	}
	return strings.Join(parts, "%")
}
func formatTemplateStringArray(they []string, when time.Time) {
	for i, x := range they {
		they[i] = formatTemplateString(x, when)
	}
}

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
	var statLogPost string
	flag.StringVar(&statLogPost, "statlog-url", "", "url to POST statlog json entries to")
	var mjpegCapturePathTemplate string
	flag.StringVar(&mjpegCapturePathTemplate, "mjpeg", "", "path to store motion mjpeg captures, %T gets timestamp")

	var configPath string
	flag.StringVar(&configPath, "config", "", "path to config json OR json literal")

	flag.Parse()

	cfg := defaultConfig
	if configPath != "" {
		if configPath[0] == '{' {
			// json literal on command line
			fin := strings.NewReader(configPath)
			dec := json.NewDecoder(fin)
			err := dec.Decode(&cfg)
			maybefail(err, "-config: bad json literal config, %v", err)
		} else {
			fin, err := os.Open(configPath)
			maybefail(err, "%s: %v", configPath, err)
			defer fin.Close()
			dec := json.NewDecoder(fin)
			err = dec.Decode(&cfg)
			maybefail(err, "%s: bad config, %v", configPath, err)
		}
	}
	if mjpegCapturePathTemplate != "" {
		cfg.MJPEGCapturePathTemplate = mjpegCapturePathTemplate
	}

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

	ctx := context.Background()

	ctx, ctxCf := context.WithCancel(ctx)
	defer ctxCf()

	var shutdownWg sync.WaitGroup

	if len(cmd) == 0 {
		fmt.Fprintf(os.Stderr, "-cmd is required")
		os.Exit(1)
	}
	jpegBlobs := make(chan []byte, 1)
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
	source.jpegBlobs = jpegBlobs
	source.Init()
	go source.Run()
	/*
		br := bufio.NewReader(source)
		go func() {
			me := breakBinaryMJPEGStream(br, jpegBlobs)
			fmt.Printf("mjpeg stream err: %v\n", me)
		}()
	*/

	js := jpegServer{
		incoming: jpegBlobs,
		cfg:      &cfg,
	}
	js.init()
	go js.reader(ctx, nil)
	go js.motionThread(ctx)
	if statLog != nil {
		js.scorestat = NewRollingKnnHistogram("s", 1000, statLog)
	} else if statLogPost != "" {
		js.scorestat = NewRollingKnnHistogramPOST("s", 1000, statLogPost)
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
