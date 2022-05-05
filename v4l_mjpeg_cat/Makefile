CFLAGS = -g -Wall -Wextra -pedantic
#-std=c99
LDFLAGS = -lv4l2 -g

v4l_mjpeg_cat:	v4l_mjpeg_cat.o
	$(CC) $< $(LDFLAGS) -o $@

v4l_mjpeg_cat.o:	v4l_mjpeg_cat.c
