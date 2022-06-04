# raspi-mjpeg-server
MJPEG http server from RaspberryPi camera

Wraps `libcamera-vid` or potentially older `raspivid` tool.

## Install

libcamera is the new RaspberryPiOS toolset as of about 2021-10. Previous RPi OS probably already have `raspivid` installed.

```sh
sudo apt-get install -y libcamera-apps
```

### Compile On RaspberryPi

Install Go [http://golang.org/dl]

```sh
go install https://github.org/brianolson/raspi-mjpeg-server@latest
```

### Cross Compile

Did you know every Go install is a fully capable cross compiler? Compile on your big fast thing and ship the fully static binary off to the little Raspberry Pi

```sh
git clone https://github.com/brianolson/raspi-mjpeg-server.git
cd raspi-mjpeg-server
GOARCH=arm GOOS=linux go build -o raspi-mjpeg-server-arm-linux
#GOARCH=arm64 GOOS=linux go build -o raspi-mjpeg-server-arm64-linux
scp -p raspi-mjpeg-server-arm-linux pi@raspberrypi.local.:~/
```

## Running

It should Just Work...

```sh
raspi-mjpeg-server -cmd '{"cmd":["libcamera-vid", "-t", "60000", "-n", "--framerate", "7", "--codec", "mjpeg", "--awb", "auto", "--width", "1920", "--height", "1080", "-o", "-"], "retry":"500ms"}' -addr :8412
```

It exposes `/jpeg` for the latest still and `/mjpeg` for a stream.
`/mjpeg` accepts `?start=-10` to start 10 seconds ago (or -5, etc). The stream should run at a slightly faster frame rate until it catches up to now.

It's possible to do development elsewhere and just run the libcamera-vid on the raspi over ssh:

```sh
raspi-mjpeg-server -cmd '{"cmd":["ssh", "pi@raspberypi.local.", "libcamera-vid", "-t", "60000", "-n", "--framerate", "7", "--codec", "mjpeg", "--awb", "auto", "--width", "1920", "--height", "1080", "-o", "-"], "retry":"2s"}' -addr :8412
```

This kind of customization allows for all of the libcamera-vid options like `--roi` region-of-interest and flips and other transformations and options.

### Video4Linux /dev/video*

Many v4l devices, and especially many USB web cameras, have an efficient built in compressor that dumps mjpeg.

```sh
(cd v4l_mjpeg_cat/; make)
./raspi-mjpeg-server -cmd '{"cmd":["v4l_mjpeg_cat/v4l_mjpeg_cat", "-c", "1000", "-d", "/dev/video0"], "retry":"5ms"}' -addr :8412
```

http://localhost:8412/mjpeg
http://localhost:8412/jpeg


## Development

http://localhost:8412/debug?fps=N&thresh=N&d=t

```
?fps=N limit frames per second
?thresh=N motion threshold
?d=t diff mode true (default off)
```


-----

Parts based on https://github.com/mattn/go-mjpeg
