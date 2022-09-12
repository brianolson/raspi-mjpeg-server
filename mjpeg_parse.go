package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
)

type ReaderAndByteReader interface {
	io.Reader
	io.ByteReader
}

// Parse binary mjpeg (just concatenated JPEG)
// (at least as produced by RaspberryPi libcamera-vid)
// out chan gets independent JPEG blobs.
// https://en.wikipedia.org/wiki/JPEG#Syntax_and_structure
func breakBinaryMJPEGStream(fin ReaderAndByteReader, out chan<- []byte) error {
	//defer close(out)
	var tag [2]byte
	var sizeb [2]byte
	var size uint16
	var next bytes.Buffer
	var prevtag [2]byte
	var offset int
	errcount := 0
	for {
		//copy(prevtag[:], tag[:])
		prevtag = tag
		_, err := fin.Read(tag[:])
		if err != nil {
			return err
		}
		if tag[0] != 0xff {
			next.Write(tag[:])
			blob := next.Bytes()
			offset += len(blob) - 2
			frp := len(blob) - 2
			if len(blob) > 100 {
				blob = blob[len(blob)-100:]
			}
			errcount++
			msg := fmt.Sprintf("Bad JPEG Tag @fr+%d abs=%d %02x%02x (prev tag %02x%02x %d) (last 100 bytes: %s)", frp, offset, tag[0], tag[1], prevtag[0], prevtag[1], size, hex.EncodeToString(blob))
			if errcount > 5 {
				return errors.New(msg)
			} else {
				debug(msg)
			}
			offset += 2
			// try to scan for next SOI
			next.Reset()
			wasff := tag[1] == 0xff
			for {
				c, err := fin.ReadByte()
				if err != nil {
					return err
				}
				offset++
				if wasff && c == 0xd8 {
					tag[0] = 0xff
					tag[1] = 0xd8
					next.Write(tag[:])
					debug("SOI 3")
					break
				}
				wasff = (c == 0xff)
			}
		}
		if tag[1] == 0xd8 {
			debug("SOI")
			// start of image
			next.Reset()
			next.Write(tag[:])
		} else if tag[1] == 0xda {
			// start of scan
			debug("scan")
			next.Write(tag[:])
			_, err = fin.Read(sizeb[:])
			if err != nil {
				return err
			}
			next.Write(sizeb[:])
			size = binary.BigEndian.Uint16(sizeb[:])
			_, err = io.CopyN(&next, fin, int64(size-2))
			if err != nil {
				return err
			}
			var c byte
			wasff := false
			was00 := false
			for {
				c, err = fin.ReadByte()
				if err != nil {
					return err
				}
				err = next.WriteByte(c)
				if err != nil {
					return err
				}
				if wasff && c == 0xd9 {
					// end of image
					blob := next.Bytes()
					debug("EOI %d bytes\n", len(blob))
					offset += len(blob)
					bc := make([]byte, len(blob))
					copy(bc, blob)
					out <- bc
					errcount = 0
					next.Reset()
					wasff = false
					break
				}
				wasff = (!was00) && (c == 0xff)
				was00 = (c == 0x00)
			}
			// skip junk (v4l2 cheap mjpeg webcam) until next ffd8 SOI
			for {
				c, err = fin.ReadByte()
				if err != nil {
					return err
				}
				if wasff && c == 0xd8 {
					tag[0] = 0xff
					tag[1] = 0xd8
					next.Write(tag[:])
					debug("SOI 2")
					break
				}
				wasff = (c == 0xff)
			}
			debug("scan done")
		} else if tag[1] == 0xdd {
			// define restart interval
			next.Write(tag[:])
			_, err = io.CopyN(&next, fin, 4)
			if err != nil {
				return err
			}
		} else if tag[1] >= 0xd0 && tag[1] <= 0xd7 {
			// "restart" tags (no size, like SOI/EOI)
			next.Write(tag[:])
		} else {
			// tag+length copy
			next.Write(tag[:])
			_, err = fin.Read(sizeb[:])
			if err != nil {
				return err
			}
			next.Write(sizeb[:])
			size = binary.BigEndian.Uint16(sizeb[:])
			debug("tag %02x%02x %d", tag[0], tag[1], size)
			_, err = io.CopyN(&next, fin, int64(size-2))
			if err != nil {
				return err
			}
		}
	}
}

// TODO: this might be necessary to replace io.CopyN above, or it might be buggy
func entropyCodingCopy(out io.Writer, fin ReaderAndByteReader, len int64) (did int, err error) {
	blob := make([]byte, len)
	got, err := fin.Read(blob)
	eof := false
	if err == io.EOF {
		eof = true
		err = nil
	}
	if err != nil {
		return 0, err
	}
	extra := 0
	wasff := false
	for _, b := range blob[:got] {
		if wasff && (b == 0) {
			extra++
		}
		wasff = (b == 0xff)
	}
	did, err = out.Write(blob[:got])
	if err != nil {
		return
	}
	if extra > 0 {
		log.Printf("entropy copy extra %d", extra)
	}
	for extra > 0 {
		extra--
		b, err := fin.ReadByte()
		if err == io.EOF {
			eof = true
			err = nil
		}
		if err != nil {
			return did, err
		}
		if wasff && (b == 0) {
			extra++
		}
		wasff = (b == 0xff)
		blob[0] = b
		xd, err := out.Write(blob[:1])
		did += xd
		if err != nil {
			return did, err
		}
	}
	if eof {
		err = io.EOF
	}
	return
}
