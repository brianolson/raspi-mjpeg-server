// Read MJPEG data from running a command
// commandMJPEGSource manages the state for running `libcamera-vid` or similar and getting MJPEG stream from it

package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"time"
)

type commandMJPEGSource struct {
	argv []string

	cmd *exec.Cmd

	ctx context.Context

	retryDelay time.Duration

	out    *io.PipeWriter
	reader *io.PipeReader

	buf []byte
}

type CmdJSON struct {
	Argv []string `json:"cmd"`

	RetryDelay string `json:"retry"` // for time.ParseDuration
}

func JsonCmd(ctx context.Context, fin io.Reader) (*commandMJPEGSource, error) {
	dec := json.NewDecoder(fin)
	var cj CmdJSON
	err := dec.Decode(&cj)
	if err != nil {
		return nil, err
	}
	out := new(commandMJPEGSource)
	out.argv = cj.Argv
	out.ctx = ctx

	if len(cj.RetryDelay) > 0 {
		out.retryDelay, err = time.ParseDuration(cj.RetryDelay)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *commandMJPEGSource) Init() {
	s.reader, s.out = io.Pipe()
	if s.retryDelay == 0 {
		s.retryDelay = time.Second
	}
	if s.ctx == nil {
		s.ctx = context.Background()
	}
}

func (s *commandMJPEGSource) Run() {
	for {
		s.runOnce()
		select {
		case <-s.ctx.Done():
			return
		default:
		}
		time.Sleep(s.retryDelay)
	}
}

func (s *commandMJPEGSource) runOnce() {
	if s.buf == nil {
		s.buf = make([]byte, 128*1024)
	}
	cmd := exec.CommandContext(s.ctx, s.argv[0], s.argv[1:]...)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("cmd setup: %v", err)
		return
	}
	defer stdout.Close()
	err = cmd.Start()
	if err != nil {
		log.Printf("cmd start: %v", err)
		return
	}
	defer waitWithTimeout(cmd, 2*time.Second)
	log.Printf("started command")
	total := 0
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}
		actual, err := stdout.Read(s.buf)
		if actual > 0 {
			_, e2 := s.out.Write(s.buf[:actual])
			if e2 != nil {
				log.Printf("cmd data internal write: %v", e2)
				return
			}
			total += actual
		}
		if err != nil {
			log.Printf("cmd read: %v (after %d)", err, total)
			return
		}
	}
}

func (s *commandMJPEGSource) Read(b []byte) (int, error) {
	l, e := s.reader.Read(b)
	//debug("mjs [%d]byte %v", l, e)
	return l, e
}

func waitWithTimeout(cmd *exec.Cmd, timeout time.Duration) {
	waitDone := make(chan int, 0)
	go func() {
		cmd.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(timeout):
		cmd.Process.Kill()
	}
}
