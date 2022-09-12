/*
based on this 'grab mjpeg to jpeg files'
https://github.com/kjetilos/mjpeg-grab
based on this grab-a-jpeg
https://github.com/twam/v4l2grab
and some example code from v4l, found here
https://gist.github.com/maxlapshin/1253534
*/


#include <assert.h>
#include <stdarg.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <getopt.h>
#include <fcntl.h>
#include <errno.h>
#include <poll.h>
#include <sys/stat.h>
//#include <linux/time.h>
#include <time.h>
#include <sys/mman.h>

#include <linux/videodev2.h>
#include <libv4l2.h>

#define VERSION "1.0"

struct buffer {
  void * start;
  size_t length;
} buffer;

// global state
static int fd = -1;
static unsigned int width = 1280;
static unsigned int height = 720;
static unsigned int fps = 30;
static char* jpegFilename = NULL;//"output.jpg";
static char* deviceName = "/dev/video0";
static unsigned int frame_count = 1000;
static bool verbose = false;
static int out_fd = STDOUT_FILENO;

struct buffer          *buffers;
static unsigned int     n_buffers;

#define IO_METHOD_READ 1
#define IO_METHOD_MMAP 2
#define IO_METHOD_USERPTR 3

#define V4LMODE IO_METHOD_MMAP


static void debug(const char* fs, ...) {
  if (!verbose) {
    return;
  }
  va_list ap;
  va_start(ap, fs);
  vfprintf(stderr, fs, ap);
  va_end(ap);
}


/**
 * Print error message and terminate programm with EXIT_FAILURE return code.
 *
 * \param s error message to print
 */
static void errno_exit(const char* s) {
  fprintf(stderr, "%s error %d, %s\n", s, errno, strerror(errno));
  exit(EXIT_FAILURE);
}

/**
 *	Do ioctl and retry if error was EINTR ("A signal was caught during the ioctl() operation."). Parameters are the same as on ioctl.
 *
 *	\param fd file descriptor
 *	\param request request
 *	\param argp argument
 *	\returns result from ioctl
 */
static int xioctl(int fd, unsigned long int request, void* argp) {
  int r;

  do {
    r = v4l2_ioctl(fd, request, argp);
  } while (-1 == r && EINTR == errno);

  return r;
}


static ssize_t parseJpegAndWrite(void* buf, ssize_t n, int out_fd) {
  uint8_t* b = (uint8_t*)buf;
  int pos = 0;
  while (pos < n) {
    if (b[pos] != 0xff) {
      fprintf(stderr, "blob[%d] bad tag %02x (%02x %02x _%02x_ %02x %02x)\n", pos, b[pos], b[pos-2], b[pos-1], b[pos], b[pos+1], b[pos+2]);
      return -1;
    }
    if (b[pos+1] == 0xd8) {
      // start of image
      pos += 2;
    } else if (b[pos+1] == 0xda) {
        // scan mode
        int skipsize = (b[pos+2] << 8) + b[pos+3];
        pos += skipsize;
        bool wasff = false;
        while ((ssize_t)pos < n) {
          if (wasff && (b[pos] == 0xd9)) {
            // end of image
            if (pos < n) {
              debug("jpeg blob ends early %d < %ld\n", pos, n);
            }
            return write(out_fd, buf, pos+1);
          }
          wasff = (b[pos] == 0xff);
          pos++;
        }
    } else if (b[pos+1] == 0xdd) {
      // define restart interval
      pos += 6;
    } else if (b[pos+1] >= 0xd0 && b[pos+1] <= 0xd7) {
      // restart tag, no length but the tag
      pos += 2;
    } else {
      // tag+length skip
      int skipsize = (b[pos+2] << 8) + b[pos+3];
      pos += skipsize + 2;
    }
  }
  debug("jpeg ended without EOI\n");
  {
    ssize_t ws;
    ws = write(out_fd, buf, pos+1);
    if (ws == n) {
      uint8_t EOI[2] = {0xff, 0xd9};
      ws = write(out_fd, EOI, 2);
      return n+ws;
    }
    return ws;
  }
}


/**
 * read single frame
 */
static int frameRead(void) {
#if V4LMODE == IO_METHOD_READ
  debug("frameRead(,,%d)\n", buffer.length);
  ssize_t n = v4l2_read(fd, buffer.start, buffer.length);

  if (n == -1) {
    switch (errno) {
      case EAGAIN:
        return 0;

      case EIO:
        // Could ignore EIO, see spec.
        // fall through
        debug("mjpeg EIO\n");
        errno_exit("read EIO");
        break;

      default:
        errno_exit("read");
    }
  }
#elif V4LMODE == IO_METHOD_MMAP
  ssize_t n;
  {
    struct v4l2_buffer buf;
    memset(&buf, 0, sizeof(buf));
    buf.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
    buf.memory = V4L2_MEMORY_MMAP;

    if (-1 == xioctl(fd, VIDIOC_DQBUF, &buf)) {
      switch (errno) {
        case EAGAIN:
          return 0;

        case EIO:
          /* Could ignore EIO, see spec. */

          /* fall through */

        default:
          errno_exit("VIDIOC_DQBUF");
      }
    }

    assert(buf.index < n_buffers);

    //process_image(buffers[buf.index].start, buf.bytesused);
    debug("mmap buf[%d] [%d]bytes\n", buf.index, buf.bytesused);
    buffer.start = buffers[buf.index].start;
    n = buf.bytesused;

    if (-1 == xioctl(fd, VIDIOC_QBUF, &buf))
      errno_exit("VIDIOC_QBUF");
  }
#elif V4LMODE == IO_METHOD_USERPTR
#error "wat"
#endif

  uint8_t* bu = (uint8_t*)buffer.start;
  int e8 = n - 16;
  debug("mjpeg blob %d bytes %02x %02x %02x %02x  %02x %02x %02x %02x ... %02x %02x %02x %02x  %02x %02x %02x %02x  %02x %02x %02x %02x  %02x %02x %02x %02x\n", n,
        bu[0], bu[1], bu[2], bu[3], bu[4], bu[5], bu[6], bu[7],
        bu[e8+0], bu[e8+1], bu[e8+2], bu[e8+3],
        bu[e8+4], bu[e8+5], bu[e8+6], bu[e8+7],
        bu[e8+8], bu[e8+9], bu[e8+10], bu[e8+11],
        bu[e8+12], bu[e8+13], bu[e8+14], bu[e8+15]
        );
  parseJpegAndWrite(buffer.start, n, out_fd);

  return 1;
}


/**
 * Read frames and process them
 */
static void mainLoop(void) {
  unsigned int count = frame_count;

  while (count > 0) {
    struct pollfd pfd = {fd, POLLIN, 0};
    int timeout = -1;

    int r = poll(&pfd, 1, timeout);

    if (r == -1)
      errno_exit("poll");

    if (frameRead())
      count--;
  }
}

#if V4LMODE == IO_METHOD_READ
static void readInit(unsigned int buffer_size)
{
  debug("buffer_size %d\n", buffer_size);
  buffer.length = buffer_size;
  buffer.start = malloc(buffer_size);

  if (!buffer.start) {
    fprintf (stderr, "Out of memory\n");
    exit(EXIT_FAILURE);
  }
}
#endif

static void init_mmap(void)
{
        struct v4l2_requestbuffers req;

        memset(&req, 0, sizeof(req));

        req.count = 4;
        req.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
        req.memory = V4L2_MEMORY_MMAP;

        if (-1 == xioctl(fd, VIDIOC_REQBUFS, &req)) {
                if (EINVAL == errno) {
                        fprintf(stderr, "%s does not support "
                                 "memory mapping\n", deviceName);
                        exit(EXIT_FAILURE);
                } else {
                        errno_exit("VIDIOC_REQBUFS");
                }
        }

        if (req.count < 2) {
                fprintf(stderr, "Insufficient buffer memory on %s\n",
                         deviceName);
                exit(EXIT_FAILURE);
        }

        buffers = calloc(req.count, sizeof(*buffers));

        if (!buffers) {
                fprintf(stderr, "Out of memory\n");
                exit(EXIT_FAILURE);
        }

        for (n_buffers = 0; n_buffers < req.count; ++n_buffers) {
                struct v4l2_buffer buf;

                memset(&buf, 0, sizeof(buf));

                buf.type        = V4L2_BUF_TYPE_VIDEO_CAPTURE;
                buf.memory      = V4L2_MEMORY_MMAP;
                buf.index       = n_buffers;

                if (-1 == xioctl(fd, VIDIOC_QUERYBUF, &buf))
                        errno_exit("VIDIOC_QUERYBUF");

                buffers[n_buffers].length = buf.length;
                buffers[n_buffers].start =
                        mmap(NULL /* start anywhere */,
                              buf.length,
                              PROT_READ | PROT_WRITE /* required */,
                              MAP_SHARED /* recommended */,
                              fd, buf.m.offset);

                if (MAP_FAILED == buffers[n_buffers].start)
                        errno_exit("mmap");
        }
}

static void deviceInit(void)
{
  struct v4l2_capability cap;
  struct v4l2_cropcap cropcap;
  struct v4l2_crop crop;
  struct v4l2_format fmt;

  if (xioctl(fd, VIDIOC_QUERYCAP, &cap) == -1) {
    if (errno == EINVAL) {
      fprintf(stderr, "%s is no V4L2 device\n",deviceName);
      exit(EXIT_FAILURE);
    } else {
      errno_exit("VIDIOC_QUERYCAP");
    }
  }

  if (!(cap.capabilities & V4L2_CAP_VIDEO_CAPTURE)) {
    fprintf(stderr, "%s is no video capture device\n",deviceName);
    exit(EXIT_FAILURE);
  }

#if V4LMODE == IO_METHOD_READ
  if (!(cap.capabilities & V4L2_CAP_READWRITE)) {
    fprintf(stderr, "%s does not support read i/o\n",deviceName);
    exit(EXIT_FAILURE);
  }
#elif (V4LMODE == IO_METHOD_USERPTR) || (V4LMODE == IO_METHOD_MMAP)
  if (!(cap.capabilities & V4L2_CAP_STREAMING)) {
    fprintf(stderr, "%s does not support streaming i/o\n", deviceName);
    exit(EXIT_FAILURE);
  }
#endif

  /* Select video input, video standard and tune here. */

  memset(&cropcap, 0, sizeof(cropcap));
  if (0 == xioctl(fd, VIDIOC_CROPCAP, &cropcap)) {
    crop.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
    crop.c = cropcap.defrect; /* reset to default */

    if (-1 == xioctl(fd, VIDIOC_S_CROP, &crop)) {
      switch (errno) {
        case EINVAL:
          /* Cropping not supported. */
          break;
        default:
          /* Errors ignored. */
          break;
      }
    }
  } else {
    /* Errors ignored. */
  }

  memset(&fmt, 0, sizeof(fmt));
  // v4l2_format
  fmt.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
  fmt.fmt.pix.width = width;
  fmt.fmt.pix.height = height;
  fmt.fmt.pix.pixelformat = V4L2_PIX_FMT_MJPEG;

  /* Set a video format for the v4l2 driver */
  if (xioctl(fd, VIDIOC_S_FMT, &fmt) == -1)
    errno_exit("VIDIOC_S_FMT");

  if (fmt.fmt.pix.pixelformat != V4L2_PIX_FMT_MJPEG) {
    fprintf(stderr,"Libv4l didn't accept MJPEG format. Can't proceed.\n");
    exit(EXIT_FAILURE);
  }

#if V4LMODE == IO_METHOD_READ
  {
    struct v4l2_streamparm frameint;
    memset(&frameint, 0, sizeof(frameint));

    /* Attempt to set the frame interval. */
    frameint.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
    frameint.parm.capture.timeperframe.numerator = 1;
    frameint.parm.capture.timeperframe.denominator = fps;
    if (xioctl(fd, VIDIOC_S_PARM, &frameint) == -1)
      fprintf(stderr,"Unable to set frame interval.\n");

    readInit(fmt.fmt.pix.sizeimage);
  }
#elif V4LMODE == IO_METHOD_MMAP
  init_mmap();
#endif
}

#if V4LMODE == IO_METHOD_MMAP
static void startMmapCapture(void) {
  enum v4l2_buf_type type;
  for (unsigned int i = 0; i < n_buffers; ++i) {
    struct v4l2_buffer buf;

    memset(&buf, 0, sizeof(buf));//CLEAR(buf);
    buf.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
    buf.memory = V4L2_MEMORY_MMAP;
    buf.index = i;

    if (-1 == xioctl(fd, VIDIOC_QBUF, &buf))
      errno_exit("VIDIOC_QBUF");
  }
  type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
  if (-1 == xioctl(fd, VIDIOC_STREAMON, &type))
    errno_exit("VIDIOC_STREAMON");
}
#endif

static void deviceClose(void)
{
#if V4LMODE == IO_METHOD_MMAP
  enum v4l2_buf_type type;
  type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
  if (-1 == xioctl(fd, VIDIOC_STREAMOFF, &type)) {
    errno_exit("VIDIOC_STREAMOFF");
  }
  for (unsigned i = 0; i < n_buffers; ++i) {
    if (-1 == munmap(buffers[i].start, buffers[i].length)) {
      errno_exit("munmap");
    }
  }
#endif
  if (v4l2_close(fd) == -1)
    errno_exit("close");

  fd = -1;
}

static void deviceOpen(void)
{
  // open device
  fd = v4l2_open(deviceName, O_RDWR | O_NONBLOCK, 0);

  // check if opening was successfull
  if (fd == -1) {
    fprintf(stderr, "Cannot open '%s': %d, %s\n", deviceName, errno, strerror(errno));
    exit(EXIT_FAILURE);
  }
}


static void usage(FILE* fp, const char* name)
{
  fprintf(fp,
          "Usage: %s [options]\n\n"
          "Options:\n"
          "-d | --device name   Video device name [/dev/video0]\n"
          "-h | --help          Print this message\n"
          "-o | --output        Set JPEG output filename [output.jpg]\n"
          "-r | --resolution    Set resolution i.e 1280x720\n"
          "-i | --interval      Set frame interval (fps)\n"
          "-V | --version       Print version\n"
          "-v | --verbose       More logging\n"
          "-c | --count         Number of jpeg's to capture [1]\n"
          "",
          name);
}

static const char short_options [] = "d:ho:r:i:vc:";

static const struct option
long_options [] = {
  { "device",     required_argument, NULL, 'd' },
  { "help",       no_argument,       NULL, 'h' },
  { "output",     required_argument, NULL, 'o' },
  { "resolution", required_argument, NULL, 'r' },
  { "interval",   required_argument, NULL, 'I' },
  { "version",	  no_argument,		   NULL, 'V' },
  { "count",      required_argument, NULL, 'c' },
  { "verbose", no_argument, NULL, 'v' },
  { 0, 0, 0, 0 }
};

int main(int argc, char **argv)
{

  for (;;) {
    int index, c = 0;

    c = getopt_long(argc, argv, short_options, long_options, &index);

    if (c == -1)
      break;

    switch (c) {
      case 0: /* getopt_long() flag */
        break;

      case 'd':
        deviceName = optarg;
        break;

      case 'h':
        // print help
        usage(stdout, argv[0]);
        exit(EXIT_SUCCESS);

      case 'o':
        // set jpeg filename
        jpegFilename = optarg;
        break;

      case 'r':
        if (sscanf(optarg, "%4ux%4u", &width, &height) != 2) {
          fprintf(stderr, "Illegal resolution argument\n");
          usage(stdout, argv[0]);
          exit(EXIT_FAILURE);
        }
        break;

      case 'i':
        // set fps
        fps = atoi(optarg);
        break;

      case 'V':
        printf("Version: %s\n", VERSION);
        exit(EXIT_SUCCESS);
        break;

      case 'v':
        verbose = true;
        break;

      case 'c':
        frame_count = atoi(optarg);
        break;

      default:
        usage(stderr, argv[0]);
        exit(EXIT_FAILURE);
    }
  }

  debug("verbose enabled\n");
  fprintf(stderr, "stderr wat\n");

  if (jpegFilename != NULL) {
    out_fd = open(jpegFilename, O_CREAT|O_WRONLY, 0666);
    if (out_fd < 0) {
      errno_exit(jpegFilename);
    }
  }

  // open and initialize device
  deviceOpen();
  deviceInit();

#if V4LMODE == IO_METHOD_MMAP
  startMmapCapture();
#endif

  // process frames
  mainLoop();

  // close device
  //deviceUninit();
#if V4LMODE == IO_METHOD_READ
  free(buffer.start);
#endif
  deviceClose();

  close(out_fd);

  return 0;
}
