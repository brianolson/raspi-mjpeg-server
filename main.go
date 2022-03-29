package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
)

func maybefail(err error, xf string, args ...interface{}) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, xf+"\n", args...)
	os.Exit(1)
}

var defaultcmd = `{"cmd":["libcamera-vid", "-t", "60000", "-n", "--framerate", "7", "--codec", "mjpeg", "--awb", "auto", "--width", "1920", "--height", "1080", "-o", "-"], "retry":"500ms"}`

func main() {
	var cmd string
	flag.StringVar(&cmd, "cmd", defaultcmd, "json {\"cmd\":[], \"retry\":\"1000ms\"}, may be json literal or filename or \"-\" for stdin json")
	var addr string
	flag.StringVar(&addr, "addr", ":8412", "host:port for http serving")

	flag.Parse()

	ctx := context.Background()

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
	go js.reader(nil)

	server := &http.Server{
		Addr:    addr,
		Handler: &js,
	}
	err = server.ListenAndServe()
	maybefail(err, "http: %v", err)
}
