package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

type ReaderAndByteReader interface {
	io.Reader
	io.ByteReader
}

// Parse binary mjpeg (just concatenated JPEG)
// (at least as produced by RaspberryPi libcamera-vid)
// out chan gets independent JPEG blobs.
func breakBinaryMJPEGStream(fin ReaderAndByteReader, out chan<- []byte) error {
	defer close(out)
	var tag [2]byte
	var sizeb [2]byte
	var size uint16
	var next bytes.Buffer
	for {
		_, err := fin.Read(tag[:])
		if err != nil {
			return err
		}
		if tag[0] != 0xff {
			return fmt.Errorf("Bad JPEG Tag %02x%02x", tag[0], tag[1])
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
					bc := make([]byte, len(blob))
					copy(bc, blob)
					out <- bc
					next.Reset()
					wasff = false
					break
				}
				wasff = (c == 0xff)
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
			_, err = io.CopyN(&next, fin, int64(size-2))
			if err != nil {
				return err
			}
		}
	}
}
