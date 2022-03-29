# raspi-mjpeg-server
MJPEG http server from RaspberryPi camera

Wraps `libcamera-vid` or potentially older `raspivid` tool.

## Install

Install Go [http://golang.org/dl]

```sh
go install https://github.org/brianolson/raspi-mjpeg-server@latest
```

libcamera is the new RaspberryPiOS toolset as of about 2021-10. Previous RPi OS probably already have `raspivid` installed.

```sh
sudo apt-get install -y libcamera-apps
```

It's possible to do development elsewhere and just run the libcamera-vid on the raspi over ssh:

```sh
raspi-mjpeg-server -cmd '{"cmd":["ssh", "pi@raspberypi.local.", "libcamera-vid", "-t", "60000", "-n", "--framerate", "7", "--codec", "mjpeg", "--awb", "auto", "--width", "1920", "--height", "1080", "-o", "-"], "retry":"2s"}' -addr :8172
```

This kind of customization allows for all of the libcamera-vid options like `--roi` region-of-interest and flips and other transformations and options.

-----

Parts based on https://github.com/mattn/go-mjpeg
