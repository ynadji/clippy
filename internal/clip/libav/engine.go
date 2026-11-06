//go:build libav

// Package libav is the clip engine that links FFmpeg's libraries directly
// instead of running an ffmpeg binary. It is built against the LGPL-only
// FFmpeg from third_party/build-ffmpeg.sh and is the engine the iOS app
// uses, where spawning a process is impossible. Video is decoded and encoded
// with the platform's hardware codecs (VideoToolbox) when available.
package libav

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/asticode/go-astiav"
	"github.com/ynadji/clippy/internal/clip"
	"github.com/ynadji/clippy/internal/proxy"
)

func init() {
	astiav.SetLogLevel(astiav.LogLevelError)
}

// Engine implements clip.Engine on top of libav*.
type Engine struct{}

// Name implements clip.Engine.
func (Engine) Name() string { return "libav" }

var (
	videoEncoderNames = []string{"h264_videotoolbox", "libx264", "libopenh264"}
	audioEncoderNames = []string{"aac_at", "aac"}
)

func findEncoder(names []string) (*astiav.Codec, error) {
	for _, n := range names {
		if c := astiav.FindEncoderByName(n); c != nil {
			return c, nil
		}
	}
	return nil, fmt.Errorf("libav: none of the encoders %v are available in this FFmpeg build", names)
}

// Preflight implements clip.Engine.
func (Engine) Preflight(r *clip.Request) error {
	if r.Copy {
		return errors.New("libav engine: --copy is not supported; use --engine exec")
	}
	if _, err := findEncoder(videoEncoderNames); err != nil {
		return err
	}
	if _, err := findEncoder(audioEncoderNames); err != nil {
		return err
	}
	if r.Subtitle != nil {
		if r.Subtitle.Codec != "" && !clip.TextSubtitle(r.Subtitle.Codec) {
			return fmt.Errorf("subtitle codec %q is bitmap-based and cannot be burned in", r.Subtitle.Codec)
		}
		if astiav.FindFilterByName("subtitles") == nil {
			return errors.New("libav engine: this FFmpeg build has no subtitles filter (libass)")
		}
	}
	return nil
}

// Describe implements clip.Engine.
func (e Engine) Describe(r *clip.Request) ([]string, error) {
	if err := e.Preflight(r); err != nil {
		return nil, err
	}
	venc, _ := findEncoder(videoEncoderNames)
	aenc, _ := findEncoder(audioEncoderNames)
	lines := []string{
		fmt.Sprintf("input:    %s", r.Input),
		fmt.Sprintf("range:    %s for %s", clip.Clock(r.Start), r.Duration),
		fmt.Sprintf("decode:   hwaccel=%s", hwaccelName(r)),
		fmt.Sprintf("video:    %s, max height %d", venc.Name(), r.MaxHeight),
		fmt.Sprintf("audio:    %s stereo 160k, stream %d", aenc.Name(), r.AudioIndex),
	}
	if r.Subtitle != nil {
		lines = append(lines, fmt.Sprintf("subtitle: burn in stream %d (%s)", r.Subtitle.Index, r.Subtitle.Codec))
	}
	lines = append(lines, fmt.Sprintf("output:   %s", r.Output))
	return lines, nil
}

func hwaccelName(r *clip.Request) string {
	switch r.HWAccel {
	case "", "auto":
		if runtime.GOOS == "darwin" || runtime.GOOS == "ios" {
			return "videotoolbox"
		}
		return ""
	case "none", "off":
		return ""
	}
	return r.HWAccel
}

// ---------------------------------------------------------------------------
// Input

const (
	avseekSize  = 0x10000
	avseekForce = 0x20000
)

// source is an opened input, either a local file or a remote file read
// through the block cache.
type source struct {
	fc    *astiav.FormatContext
	ioc   *astiav.IOContext
	cache *proxy.Cache
	pos   int64
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func parseHeaders(raw string) http.Header {
	h := http.Header{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, ":"); ok {
			h.Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
	return h
}

// cachePool keeps the block cache of the most recently used remote file so
// that consecutive calls on it (two thumbnails per scrub in the app, then
// the clip itself) reuse the blocks already fetched instead of each opening
// their own cache. A Cache is safe for concurrent readers.
var cachePool struct {
	mu    sync.Mutex
	key   string
	cache *proxy.Cache
}

// maxCacheBlocks bounds memory per file: 48 blocks of 4 MiB is 192 MiB.
const maxCacheBlocks = 48

func sharedCache(ctx context.Context, input, headers string) (*proxy.Cache, error) {
	key := input + "\x00" + headers
	cachePool.mu.Lock()
	if cachePool.key == key && cachePool.cache != nil {
		c := cachePool.cache
		cachePool.mu.Unlock()
		return c, nil
	}
	cachePool.mu.Unlock()
	c, err := proxy.NewCache(ctx, input, parseHeaders(headers), proxy.WithMaxBlocks(maxCacheBlocks))
	if err != nil {
		return nil, err
	}
	cachePool.mu.Lock()
	defer cachePool.mu.Unlock()
	if cachePool.key == key && cachePool.cache != nil { // lost a race; use the winner
		return cachePool.cache, nil
	}
	cachePool.key, cachePool.cache = key, c
	return c, nil
}

func openSource(ctx context.Context, input, headers string) (*source, error) {
	s := &source{fc: astiav.AllocFormatContext()}
	if s.fc == nil {
		return nil, errors.New("libav: allocating format context failed")
	}
	var err error
	if isURL(input) {
		if s.cache, err = sharedCache(ctx, input, headers); err != nil {
			s.fc.Free()
			return nil, err
		}
		if s.ioc, err = astiav.AllocIOContext(256<<10, false, s.read, s.seek, nil); err != nil {
			s.fc.Free()
			return nil, err
		}
		s.fc.SetPb(s.ioc)
		err = s.fc.OpenInput("", nil, nil)
	} else {
		err = s.fc.OpenInput(input, nil, nil)
	}
	if err != nil {
		s.fc.Free()
		if s.ioc != nil {
			s.ioc.Free()
		}
		return nil, fmt.Errorf("libav: open %s: %w", input, err)
	}
	if err := s.fc.FindStreamInfo(nil); err != nil {
		s.close()
		return nil, fmt.Errorf("libav: stream info: %w", err)
	}
	return s, nil
}

func (s *source) read(b []byte) (int, error) {
	n, err := s.cache.ReadAt(b, s.pos)
	s.pos += int64(n)
	if n == 0 && err != nil {
		if errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		return 0, err
	}
	return n, nil
}

func (s *source) seek(off int64, whence int) (int64, error) {
	whence &^= avseekForce
	switch whence {
	case avseekSize:
		return s.cache.Size(), nil
	case io.SeekStart:
		s.pos = off
	case io.SeekCurrent:
		s.pos += off
	case io.SeekEnd:
		s.pos = s.cache.Size() + off
	default:
		return 0, fmt.Errorf("libav: bad whence %d", whence)
	}
	return s.pos, nil
}

func (s *source) close() {
	s.fc.CloseInput()
	s.fc.Free()
	if s.ioc != nil {
		s.ioc.Free()
	}
}

// seekTo positions the demuxer at the last keyframe before t.
func (s *source) seekTo(t time.Duration) error {
	ts := int64(t / time.Microsecond) // AV_TIME_BASE is microseconds
	return s.fc.SeekFrame(-1, ts, astiav.NewSeekFlags(astiav.SeekFlagBackward))
}

// tb converts a duration into a stream's time base.
func tb(d time.Duration, r astiav.Rational) int64 {
	return astiav.RescaleQ(int64(d/time.Microsecond), astiav.NewRational(1, astiav.TimeBase), r)
}

func secondsOf(ts int64, r astiav.Rational) time.Duration {
	return time.Duration(float64(ts) * float64(r.Num()) / float64(r.Den()) * float64(time.Second))
}

func pickStreams(fc *astiav.FormatContext, audioIndex int) (vs, as *astiav.Stream, err error) {
	vs, _, err = fc.FindBestStream(astiav.MediaTypeVideo, -1, -1)
	if err != nil || vs == nil {
		return nil, nil, errors.New("libav: no video stream")
	}
	if audioIndex >= 0 {
		for _, s := range fc.Streams() {
			if s.Index() == audioIndex && s.CodecParameters().MediaType() == astiav.MediaTypeAudio {
				as = s
			}
		}
		if as == nil {
			return nil, nil, fmt.Errorf("libav: stream %d is not an audio stream", audioIndex)
		}
	} else if s, _, err := fc.FindBestStream(astiav.MediaTypeAudio, -1, -1); err == nil {
		as = s
	}
	return vs, as, nil
}

// ---------------------------------------------------------------------------
// Decoding

type decoder struct {
	cc      *astiav.CodecContext
	hw      *astiav.HardwareDeviceContext
	hwFmt   astiav.PixelFormat
	frame   *astiav.Frame
	swFrame *astiav.Frame
}

func openDecoder(fc *astiav.FormatContext, s *astiav.Stream, hwaccel string) (*decoder, error) {
	codec := astiav.FindDecoder(s.CodecParameters().CodecID())
	if codec == nil {
		return nil, fmt.Errorf("libav: no decoder for %s", s.CodecParameters().CodecID().Name())
	}
	d := &decoder{cc: astiav.AllocCodecContext(codec), hwFmt: astiav.PixelFormatNone}
	if d.cc == nil {
		return nil, errors.New("libav: allocating codec context failed")
	}
	if err := s.CodecParameters().ToCodecContext(d.cc); err != nil {
		d.close()
		return nil, err
	}
	isVideo := s.CodecParameters().MediaType() == astiav.MediaTypeVideo
	if isVideo {
		d.cc.SetFramerate(fc.GuessFrameRate(s, nil))
		if hwaccel != "" {
			if t := astiav.FindHardwareDeviceTypeByName(hwaccel); t != astiav.HardwareDeviceTypeNone {
				if hdc, err := astiav.CreateHardwareDeviceContext(t, "", nil, 0); err == nil {
					d.hw = hdc
					d.hwFmt = hwPixelFormat(codec, t)
					if d.hwFmt != astiav.PixelFormatNone {
						d.cc.SetHardwareDeviceContext(hdc)
						hwFmt := d.hwFmt
						d.cc.SetPixelFormatCallback(func(pfs []astiav.PixelFormat) astiav.PixelFormat {
							for _, pf := range pfs {
								if pf == hwFmt {
									return pf
								}
							}
							for _, pf := range pfs { // software fallback
								if pf != hwFmt {
									return pf
								}
							}
							return astiav.PixelFormatNone
						})
					}
				}
			}
		}
	}
	if err := d.cc.Open(codec, nil); err != nil {
		d.close()
		return nil, fmt.Errorf("libav: open decoder %s: %w", codec.Name(), err)
	}
	d.frame = astiav.AllocFrame()
	d.swFrame = astiav.AllocFrame()
	return d, nil
}

// hwPixelFormat returns the pixel format the codec produces for a hardware
// device type, or none if the codec has no such hwaccel.
func hwPixelFormat(codec *astiav.Codec, t astiav.HardwareDeviceType) astiav.PixelFormat {
	for _, hc := range codec.HardwareConfigs() {
		if hc.HardwareDeviceType() == t && hc.MethodFlags().Has(astiav.CodecHardwareConfigMethodFlagHwDeviceCtx) {
			return hc.PixelFormat()
		}
	}
	return astiav.PixelFormatNone
}

func (d *decoder) close() {
	if d.frame != nil {
		d.frame.Free()
	}
	if d.swFrame != nil {
		d.swFrame.Free()
	}
	if d.cc != nil {
		d.cc.Free()
	}
	if d.hw != nil {
		d.hw.Free()
	}
}

// decode sends a packet (nil to flush) and hands every resulting frame to
// fn, transferring hardware frames to system memory first.
func (d *decoder) decode(pkt *astiav.Packet, fn func(*astiav.Frame) error) error {
	if err := d.cc.SendPacket(pkt); err != nil && !errors.Is(err, astiav.ErrEof) {
		return fmt.Errorf("libav: send packet: %w", err)
	}
	for {
		if err := d.cc.ReceiveFrame(d.frame); err != nil {
			if errors.Is(err, astiav.ErrEof) || errors.Is(err, astiav.ErrEagain) {
				return nil
			}
			return fmt.Errorf("libav: receive frame: %w", err)
		}
		f := d.frame
		if d.hwFmt != astiav.PixelFormatNone && f.PixelFormat() == d.hwFmt {
			d.swFrame.Unref()
			if err := f.TransferHardwareData(d.swFrame); err != nil {
				f.Unref()
				return fmt.Errorf("libav: hardware frame transfer: %w", err)
			}
			d.swFrame.SetPts(f.Pts())
			d.swFrame.SetSampleAspectRatio(f.SampleAspectRatio())
			f = d.swFrame
		}
		err := fn(f)
		f.Unref()
		d.frame.Unref()
		if err != nil {
			return err
		}
	}
}

// ---------------------------------------------------------------------------
// Filtering

var errGraphDone = errors.New("graph reached end of trim range")

type graph struct {
	g    *astiav.FilterGraph
	src  *astiav.BuffersrcFilterContext
	sink *astiav.BuffersinkFilterContext
	out  *astiav.Frame
	done bool
}

func newGraph(audio bool, configure func(*astiav.BuffersrcFilterContextParameters), spec string) (*graph, error) {
	g := &graph{g: astiav.AllocFilterGraph(), out: astiav.AllocFrame()}
	if g.g == nil {
		return nil, errors.New("libav: allocating filter graph failed")
	}
	srcName, sinkName := "buffer", "buffersink"
	if audio {
		srcName, sinkName = "abuffer", "abuffersink"
	}
	src, sink := astiav.FindFilterByName(srcName), astiav.FindFilterByName(sinkName)
	if src == nil || sink == nil {
		g.close()
		return nil, errors.New("libav: buffer filters missing")
	}
	var err error
	if g.src, err = g.g.NewBuffersrcFilterContext(src, "in"); err != nil {
		g.close()
		return nil, err
	}
	if g.sink, err = g.g.NewBuffersinkFilterContext(sink, "out"); err != nil {
		g.close()
		return nil, err
	}
	params := astiav.AllocBuffersrcFilterContextParameters()
	defer params.Free()
	configure(params)
	if err = g.src.SetParameters(params); err != nil {
		g.close()
		return nil, err
	}
	if err = g.src.Initialize(nil); err != nil {
		g.close()
		return nil, err
	}
	outputs, inputs := astiav.AllocFilterInOut(), astiav.AllocFilterInOut()
	defer outputs.Free()
	defer inputs.Free()
	outputs.SetName("in")
	outputs.SetFilterContext(g.src.FilterContext())
	outputs.SetPadIdx(0)
	outputs.SetNext(nil)
	inputs.SetName("out")
	inputs.SetFilterContext(g.sink.FilterContext())
	inputs.SetPadIdx(0)
	inputs.SetNext(nil)
	if err = g.g.Parse(spec, inputs, outputs); err != nil {
		g.close()
		return nil, fmt.Errorf("libav: filter graph %q: %w", spec, err)
	}
	if err = g.g.Configure(); err != nil {
		g.close()
		return nil, fmt.Errorf("libav: configure filter graph %q: %w", spec, err)
	}
	return g, nil
}

func (g *graph) close() {
	if g.out != nil {
		g.out.Free()
	}
	if g.g != nil {
		g.g.Free()
	}
}

// push feeds a frame (nil to flush) and hands filtered frames to fn. It
// returns errGraphDone once the sink reports EOF, which trim filters do
// after the end of the range.
func (g *graph) push(f *astiav.Frame, fn func(*astiav.Frame) error) error {
	if g.done {
		return errGraphDone
	}
	if err := g.src.AddFrame(f, astiav.NewBuffersrcFlags(astiav.BuffersrcFlagKeepRef)); err != nil && !errors.Is(err, astiav.ErrEof) {
		return fmt.Errorf("libav: add frame to graph: %w", err)
	}
	for {
		if err := g.sink.GetFrame(g.out, astiav.NewBuffersinkFlags()); err != nil {
			if errors.Is(err, astiav.ErrEagain) {
				return nil
			}
			if errors.Is(err, astiav.ErrEof) {
				g.done = true
				return errGraphDone
			}
			return fmt.Errorf("libav: get frame from graph: %w", err)
		}
		err := fn(g.out)
		g.out.Unref()
		if err != nil {
			return err
		}
	}
}

// ---------------------------------------------------------------------------
// Encoding

type encoder struct {
	name string
	cc   *astiav.CodecContext
	st   *astiav.Stream
	pkt  *astiav.Packet
}

func (e *encoder) close() {
	if e.pkt != nil {
		e.pkt.Free()
	}
	if e.cc != nil {
		e.cc.Free()
	}
}

// encode sends a frame (nil to flush) and writes the resulting packets.
func (e *encoder) encode(out *astiav.FormatContext, f *astiav.Frame) error {
	if err := e.cc.SendFrame(f); err != nil && !errors.Is(err, astiav.ErrEof) {
		return fmt.Errorf("libav: send frame to %s: %w", e.name, err)
	}
	for {
		if err := e.cc.ReceivePacket(e.pkt); err != nil {
			if errors.Is(err, astiav.ErrEof) || errors.Is(err, astiav.ErrEagain) {
				return nil
			}
			return fmt.Errorf("libav: receive packet from %s: %w", e.name, err)
		}
		e.pkt.SetStreamIndex(e.st.Index())
		e.pkt.RescaleTs(e.cc.TimeBase(), e.st.TimeBase())
		err := out.WriteInterleavedFrame(e.pkt)
		e.pkt.Unref()
		if err != nil {
			return fmt.Errorf("libav: write packet: %w", err)
		}
	}
}

func setPrivateOptions(cc *astiav.CodecContext, opts map[string]string) {
	pd := cc.PrivateData()
	if pd == nil {
		return
	}
	for k, v := range opts {
		_ = pd.Options().Set(k, v, astiav.NewOptionSearchFlags())
	}
}

// outputSize fits the picture within the 16:9 frame of the given height.
func outputSize(w, h, maxHeight int) (int, int) { return clip.FitWithin(w, h, maxHeight) }

func videoBitrate(h int) int64 {
	switch {
	case h >= 2000:
		return 20_000_000
	case h >= 1000:
		return 8_000_000
	case h >= 700:
		return 4_500_000
	default:
		return 2_500_000
	}
}

func openVideoEncoder(out *astiav.FormatContext, dec *decoder, vs *astiav.Stream, maxHeight int) (*encoder, int, int, error) {
	codec, err := findEncoder(videoEncoderNames)
	if err != nil {
		return nil, 0, 0, err
	}
	w, h := outputSize(dec.cc.Width(), dec.cc.Height(), maxHeight)
	e := &encoder{name: codec.Name(), cc: astiav.AllocCodecContext(codec), pkt: astiav.AllocPacket()}
	e.cc.SetWidth(w)
	e.cc.SetHeight(h)
	e.cc.SetPixelFormat(astiav.PixelFormatNv12)
	if pfs := codec.SupportedPixelFormats(); len(pfs) > 0 {
		e.cc.SetPixelFormat(pfs[0])
		for _, pf := range pfs {
			if pf == astiav.PixelFormatNv12 || pf == astiav.PixelFormatYuv420P {
				e.cc.SetPixelFormat(pf)
				break
			}
		}
	}
	e.cc.SetSampleAspectRatio(dec.cc.SampleAspectRatio())
	e.cc.SetTimeBase(vs.TimeBase())
	fr := dec.cc.Framerate()
	if fr.Num() <= 0 {
		fr = astiav.NewRational(30, 1)
	}
	e.cc.SetFramerate(fr)
	e.cc.SetGopSize(int(2 * float64(fr.Num()) / float64(fr.Den())))
	e.cc.SetBitRate(videoBitrate(h))
	e.cc.SetColorRange(dec.cc.ColorRange())
	if out.OutputFormat().Flags().Has(astiav.IOFormatFlagGlobalheader) {
		e.cc.SetFlags(e.cc.Flags().Add(astiav.CodecContextFlagGlobalHeader))
	}
	switch codec.Name() {
	case "h264_videotoolbox":
		setPrivateOptions(e.cc, map[string]string{"allow_sw": "1", "realtime": "0", "profile": "high"})
	case "libx264":
		setPrivateOptions(e.cc, map[string]string{"preset": "veryfast", "crf": "20"})
	}
	if err := e.cc.Open(codec, nil); err != nil {
		e.close()
		return nil, 0, 0, fmt.Errorf("libav: open %s: %w", codec.Name(), err)
	}
	if e.st = out.NewStream(nil); e.st == nil {
		e.close()
		return nil, 0, 0, errors.New("libav: new video stream failed")
	}
	if err := e.st.CodecParameters().FromCodecContext(e.cc); err != nil {
		e.close()
		return nil, 0, 0, err
	}
	e.st.SetTimeBase(e.cc.TimeBase())
	return e, w, h, nil
}

func openAudioEncoder(out *astiav.FormatContext, dec *decoder) (*encoder, error) {
	codec, err := findEncoder(audioEncoderNames)
	if err != nil {
		return nil, err
	}
	e := &encoder{name: codec.Name(), cc: astiav.AllocCodecContext(codec), pkt: astiav.AllocPacket()}
	sr := dec.cc.SampleRate()
	if rates := codec.SupportedSampleRates(); len(rates) > 0 {
		ok := false
		for _, r := range rates {
			if r == sr {
				ok = true
			}
		}
		if !ok {
			sr = 48000
		}
	}
	e.cc.SetSampleRate(sr)
	e.cc.SetChannelLayout(astiav.ChannelLayoutStereo)
	sf := astiav.SampleFormatFltp
	if sfs := codec.SupportedSampleFormats(); len(sfs) > 0 {
		sf = sfs[0]
	}
	e.cc.SetSampleFormat(sf)
	e.cc.SetTimeBase(astiav.NewRational(1, sr))
	e.cc.SetBitRate(160_000)
	if out.OutputFormat().Flags().Has(astiav.IOFormatFlagGlobalheader) {
		e.cc.SetFlags(e.cc.Flags().Add(astiav.CodecContextFlagGlobalHeader))
	}
	if err := e.cc.Open(codec, nil); err != nil {
		e.close()
		return nil, fmt.Errorf("libav: open %s: %w", codec.Name(), err)
	}
	if e.st = out.NewStream(nil); e.st == nil {
		e.close()
		return nil, errors.New("libav: new audio stream failed")
	}
	if err := e.st.CodecParameters().FromCodecContext(e.cc); err != nil {
		e.close()
		return nil, err
	}
	e.st.SetTimeBase(e.cc.TimeBase())
	return e, nil
}

// ---------------------------------------------------------------------------
// Run

// Run implements clip.Engine.
func (Engine) Run(ctx context.Context, r *clip.Request, logw io.Writer, progress clip.Progress) (err error) {
	if logw != nil {
		astiav.SetLogCallback(func(c astiav.Classer, l astiav.LogLevel, _, msg string) {
			if l <= astiav.LogLevelWarning {
				fmt.Fprintf(logw, "ffmpeg: %s\n", strings.TrimSpace(msg))
			}
		})
		defer astiav.SetLogCallback(nil)
	}
	hw := hwaccelName(r)

	src, err := openSource(ctx, r.Input, r.Headers)
	if err != nil {
		return err
	}
	defer src.close()
	vs, as, err := pickStreams(src.fc, r.AudioIndex)
	if err != nil {
		return err
	}
	end := r.Start + r.Duration

	// Subtitles: extract and shift the cues into a temp file for libass.
	var subFile string
	if r.Subtitle != nil {
		dir, err := os.MkdirTemp("", "clippy-libav-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		if subFile, err = extractSubtitles(ctx, src, r, dir); err != nil {
			return err
		}
	}

	// Only demux the streams we use.
	for _, s := range src.fc.Streams() {
		if s.Index() != vs.Index() && (as == nil || s.Index() != as.Index()) {
			s.SetDiscard(astiav.DiscardAll)
		} else {
			s.SetDiscard(astiav.DiscardDefault)
		}
	}

	vdec, err := openDecoder(src.fc, vs, hw)
	if err != nil {
		return err
	}
	defer vdec.close()
	var adec *decoder
	if as != nil {
		if adec, err = openDecoder(src.fc, as, ""); err != nil {
			return err
		}
		defer adec.close()
	}

	// Output container and encoders.
	out, err := astiav.AllocOutputFormatContext(nil, "", r.Output)
	if err != nil || out == nil {
		return fmt.Errorf("libav: output %s: %v", r.Output, err)
	}
	defer out.Free()
	venc, ow, oh, err := openVideoEncoder(out, vdec, vs, r.MaxHeight)
	if err != nil {
		return err
	}
	defer venc.close()
	var aenc *encoder
	if adec != nil {
		if aenc, err = openAudioEncoder(out, adec); err != nil {
			return err
		}
		defer aenc.close()
	}
	ioc, err := astiav.OpenIOContext(r.Output, astiav.NewIOContextFlags(astiav.IOContextFlagWrite), nil, nil)
	if err != nil {
		return fmt.Errorf("libav: create %s: %w", r.Output, err)
	}
	out.SetPb(ioc)
	defer func() {
		if cerr := ioc.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	muxOpts := astiav.NewDictionary()
	defer muxOpts.Free()
	if strings.EqualFold(filepath.Ext(r.Output), ".mp4") || strings.EqualFold(filepath.Ext(r.Output), ".m4v") || strings.EqualFold(filepath.Ext(r.Output), ".mov") {
		_ = muxOpts.Set("movflags", "+faststart", astiav.NewDictionaryFlags())
	}
	if err := out.WriteHeader(muxOpts); err != nil {
		return fmt.Errorf("libav: write header: %w", err)
	}

	// Filter graphs are built on the first frame, when the decoded pixel
	// format (hardware or software) is known.
	vStart, vEnd := tb(r.Start, vs.TimeBase()), tb(end, vs.TimeBase())
	var vgraph, agraph *graph
	defer func() {
		if vgraph != nil {
			vgraph.close()
		}
		if agraph != nil {
			agraph.close()
		}
	}()
	videoDone, audioDone := false, as == nil

	onVideoOut := func(f *astiav.Frame) error {
		f.SetPictureType(astiav.PictureTypeNone)
		if progress != nil {
			done := secondsOf(f.Pts(), vs.TimeBase())
			if done > r.Duration {
				done = r.Duration
			}
			progress(done, r.Duration)
		}
		return venc.encode(out, f)
	}
	onVideo := func(f *astiav.Frame) error {
		if vgraph == nil {
			var filters []string
			filters = append(filters,
				fmt.Sprintf("trim=start_pts=%d:end_pts=%d", vStart, vEnd),
				fmt.Sprintf("setpts=PTS-%d", vStart))
			if f.Width() != ow || f.Height() != oh {
				filters = append(filters, fmt.Sprintf("scale=%d:%d:flags=bicubic", ow, oh))
			}
			if subFile != "" {
				filters = append(filters, "subtitles="+filterEscape(subFile))
			}
			filters = append(filters, "format="+venc.cc.PixelFormat().Name())
			var gerr error
			vgraph, gerr = newGraph(false, func(p *astiav.BuffersrcFilterContextParameters) {
				p.SetWidth(f.Width())
				p.SetHeight(f.Height())
				p.SetPixelFormat(f.PixelFormat())
				p.SetSampleAspectRatio(f.SampleAspectRatio())
				p.SetTimeBase(vs.TimeBase())
				p.SetColorRange(f.ColorRange())
			}, strings.Join(filters, ","))
			if gerr != nil {
				return gerr
			}
		}
		if err := vgraph.push(f, onVideoOut); err != nil {
			if errors.Is(err, errGraphDone) {
				videoDone = true
				return nil
			}
			return err
		}
		return nil
	}

	// Audio: trim precisely, convert to the encoder's format, then feed the
	// encoder fixed-size frames through a FIFO.
	debug := os.Getenv("CLIPPY_LIBAV_DEBUG") != "" && logw != nil
	nAudioIn, nAudioOut := 0, 0
	var fifo *astiav.AudioFifo
	var aframe *astiav.Frame
	var samplesOut int64
	defer func() {
		if fifo != nil {
			fifo.Free()
		}
		if aframe != nil {
			aframe.Free()
		}
	}()
	drainFifo := func(all bool) error {
		fs := aenc.cc.FrameSize()
		if fs <= 0 {
			fs = 1024
		}
		for fifo.Size() >= fs || (all && fifo.Size() > 0) {
			n := fs
			if fifo.Size() < n {
				n = fifo.Size()
			}
			aframe.Unref()
			aframe.SetNbSamples(n)
			aframe.SetSampleFormat(aenc.cc.SampleFormat())
			aframe.SetChannelLayout(aenc.cc.ChannelLayout())
			aframe.SetSampleRate(aenc.cc.SampleRate())
			if err := aframe.AllocBuffer(0); err != nil {
				return err
			}
			if _, err := fifo.Read(aframe); err != nil {
				return err
			}
			aframe.SetPts(samplesOut)
			samplesOut += int64(n)
			if err := aenc.encode(out, aframe); err != nil {
				return err
			}
		}
		return nil
	}
	onAudioOut := func(f *astiav.Frame) error {
		nAudioOut++
		if debug && nAudioOut <= 6 {
			fmt.Fprintf(logw, "audio out #%d: pts=%d rate=%d samples=%d\n", nAudioOut, f.Pts(), f.SampleRate(), f.NbSamples())
		}
		if fifo == nil {
			fifo = astiav.AllocAudioFifo(aenc.cc.SampleFormat(), aenc.cc.ChannelLayout().Channels(), 8192)
			aframe = astiav.AllocFrame()
		}
		if _, err := fifo.Write(f); err != nil {
			return err
		}
		return drainFifo(false)
	}
	// Decoders can change sample rate or layout mid-stream (HE-AAC switches
	// once SBR is detected a few frames after a seek), so the graph is
	// rebuilt whenever the frame properties differ from what it was built for.
	type audioProps struct {
		rate   int
		format astiav.SampleFormat
		layout string
	}
	var agraphProps audioProps
	onAudio := func(f *astiav.Frame) error {
		props := audioProps{f.SampleRate(), f.SampleFormat(), f.ChannelLayout().String()}
		nAudioIn++
		if debug && nAudioIn <= 12 {
			fmt.Fprintf(logw, "audio in #%d: pts=%d rate=%d fmt=%s layout=%s samples=%d (trim %d..%d tb %d/%d)\n",
				nAudioIn, f.Pts(), props.rate, props.format.Name(), props.layout, f.NbSamples(),
				tb(r.Start, as.TimeBase()), tb(end, as.TimeBase()), as.TimeBase().Num(), as.TimeBase().Den())
		}
		if agraph != nil && props != agraphProps {
			if err := agraph.push(nil, onAudioOut); err != nil && !errors.Is(err, errGraphDone) {
				return err
			}
			agraph.close()
			agraph = nil
		}
		if agraph == nil {
			agraphProps = props
			// atrim's start_pts/end_pts are in samples (unlike trim's), so
			// give it seconds.
			spec := fmt.Sprintf("atrim=start=%.6f:end=%.6f,asetpts=PTS-STARTPTS,aformat=sample_fmts=%s:channel_layouts=stereo:sample_rates=%d",
				r.Start.Seconds(), end.Seconds(), aenc.cc.SampleFormat().Name(), aenc.cc.SampleRate())
			var gerr error
			agraph, gerr = newGraph(true, func(p *astiav.BuffersrcFilterContextParameters) {
				p.SetChannelLayout(f.ChannelLayout())
				p.SetSampleFormat(f.SampleFormat())
				p.SetSampleRate(f.SampleRate())
				p.SetTimeBase(as.TimeBase())
			}, spec)
			if gerr != nil {
				return gerr
			}
		}
		if err := agraph.push(f, onAudioOut); err != nil {
			if errors.Is(err, errGraphDone) {
				if debug {
					fmt.Fprintf(logw, "audio graph done after %d in / %d out\n", nAudioIn, nAudioOut)
				}
				audioDone = true
				return nil
			}
			return err
		}
		return nil
	}

	if err := src.seekTo(r.Start); err != nil {
		return fmt.Errorf("libav: seek to %s: %w", clip.Clock(r.Start), err)
	}
	pkt := astiav.AllocPacket()
	defer pkt.Free()
	for !(videoDone && audioDone) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := src.fc.ReadFrame(pkt); err != nil {
			if errors.Is(err, astiav.ErrEof) {
				break
			}
			return fmt.Errorf("libav: read: %w", err)
		}
		var perr error
		switch {
		case pkt.StreamIndex() == vs.Index() && !videoDone:
			perr = vdec.decode(pkt, onVideo)
		case as != nil && pkt.StreamIndex() == as.Index() && !audioDone:
			perr = adec.decode(pkt, onAudio)
		}
		pkt.Unref()
		if perr != nil {
			return perr
		}
	}

	// Flush: decoders, graphs, FIFO, encoders.
	if !videoDone {
		if err := vdec.decode(nil, onVideo); err != nil {
			return err
		}
		if vgraph != nil && !vgraph.done {
			if err := vgraph.push(nil, onVideoOut); err != nil && !errors.Is(err, errGraphDone) {
				return err
			}
		}
	}
	if as != nil && !audioDone {
		if err := adec.decode(nil, onAudio); err != nil {
			return err
		}
		if agraph != nil && !agraph.done {
			if err := agraph.push(nil, onAudioOut); err != nil && !errors.Is(err, errGraphDone) {
				return err
			}
		}
	}
	if fifo != nil {
		if err := drainFifo(true); err != nil {
			return err
		}
	}
	if err := venc.encode(out, nil); err != nil {
		return err
	}
	if aenc != nil {
		if err := aenc.encode(out, nil); err != nil {
			return err
		}
	}
	if err := out.WriteTrailer(); err != nil {
		return fmt.Errorf("libav: write trailer: %w", err)
	}
	if progress != nil {
		progress(r.Duration, r.Duration)
	}
	if src.cache != nil && logw != nil {
		fetches, bytes := src.cache.Stats()
		fmt.Fprintf(logw, "fetched %.1f MB from the server in %d requests\n", float64(bytes)/1e6, fetches)
	}
	return nil
}

// filterEscape quotes a path for use inside a filter graph.
func filterEscape(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, `:`, `\:`, `[`, `\[`, `]`, `\]`, `,`, `\,`, `;`, `\;`)
	return "'" + r.Replace(p) + "'"
}

// ---------------------------------------------------------------------------
// Subtitles

// subtitleMuxer maps a text subtitle codec to the container libass (via the
// subtitles filter) can read it back from.
func subtitleMuxer(id astiav.CodecID) (format, ext string, ok bool) {
	switch id {
	case astiav.CodecIDAss:
		return "ass", ".ass", true
	case astiav.CodecIDSubrip:
		return "srt", ".srt", true
	case astiav.CodecIDWebvtt:
		return "webvtt", ".vtt", true
	case astiav.CodecIDMovText:
		return "mp4", ".mp4", true
	}
	return "", "", false
}

// extractSubtitles remuxes the cues overlapping the clip range into a file,
// with timestamps shifted so the clip starts at zero.
func extractSubtitles(ctx context.Context, src *source, r *clip.Request, dir string) (string, error) {
	in := src
	var st *astiav.Stream
	if r.Subtitle.File != "" {
		ext, err := openSource(ctx, r.Subtitle.File, "")
		if err != nil {
			return "", err
		}
		defer ext.close()
		in = ext
		for _, s := range ext.fc.Streams() {
			if s.CodecParameters().MediaType() == astiav.MediaTypeSubtitle {
				st = s
				break
			}
		}
	} else {
		for _, s := range src.fc.Streams() {
			if s.Index() == r.Subtitle.Index && s.CodecParameters().MediaType() == astiav.MediaTypeSubtitle {
				st = s
			}
		}
	}
	if st == nil {
		return "", fmt.Errorf("libav: subtitle stream %d not found", r.Subtitle.Index)
	}
	format, ext, ok := subtitleMuxer(st.CodecParameters().CodecID())
	if !ok {
		return "", fmt.Errorf("libav: cannot burn in %s subtitles", st.CodecParameters().CodecID().Name())
	}
	path := filepath.Join(dir, "subs"+ext)

	out, err := astiav.AllocOutputFormatContext(nil, format, path)
	if err != nil || out == nil {
		return "", fmt.Errorf("libav: subtitle muxer %s: %v", format, err)
	}
	defer out.Free()
	ost := out.NewStream(nil)
	if ost == nil {
		return "", errors.New("libav: new subtitle stream failed")
	}
	if err := st.CodecParameters().Copy(ost.CodecParameters()); err != nil {
		return "", err
	}
	ost.SetTimeBase(st.TimeBase())
	ioc, err := astiav.OpenIOContext(path, astiav.NewIOContextFlags(astiav.IOContextFlagWrite), nil, nil)
	if err != nil {
		return "", err
	}
	defer ioc.Close()
	out.SetPb(ioc)
	if err := out.WriteHeader(nil); err != nil {
		return "", fmt.Errorf("libav: subtitle header: %w", err)
	}

	for _, s := range in.fc.Streams() {
		if s.Index() == st.Index() {
			s.SetDiscard(astiav.DiscardDefault)
		} else {
			s.SetDiscard(astiav.DiscardAll)
		}
	}
	if in == src {
		if err := src.seekTo(r.Start); err != nil {
			return "", err
		}
	}
	start, end := tb(r.Start, st.TimeBase()), tb(r.Start+r.Duration, st.TimeBase())
	if err := remuxSubtitles(ctx, in, st, ost, out, start, end); err != nil {
		return "", err
	}
	if err := out.WriteTrailer(); err != nil {
		return "", err
	}
	return path, nil
}

// remuxSubtitles copies the cues of st that overlap [start, end) (in st's
// time base) into ost, shifted so that start becomes zero and clamped to
// the range.
func remuxSubtitles(ctx context.Context, in *source, st, ost *astiav.Stream, out *astiav.FormatContext, start, end int64) error {
	pkt := astiav.AllocPacket()
	defer pkt.Free()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := in.fc.ReadFrame(pkt); err != nil {
			if errors.Is(err, astiav.ErrEof) {
				return nil
			}
			return err
		}
		if pkt.StreamIndex() != st.Index() {
			pkt.Unref()
			continue
		}
		pts, dur := pkt.Pts(), pkt.Duration()
		if pts >= end {
			pkt.Unref()
			return nil
		}
		if pts+dur <= start {
			pkt.Unref()
			continue
		}
		pts -= start
		if pts < 0 {
			dur += pts
			pts = 0
		}
		if pts+dur > end-start {
			dur = end - start - pts
		}
		pkt.SetPts(pts)
		pkt.SetDts(pts)
		pkt.SetDuration(dur)
		pkt.SetStreamIndex(ost.Index())
		pkt.RescaleTs(st.TimeBase(), ost.TimeBase())
		err := out.WriteInterleavedFrame(pkt)
		pkt.Unref()
		if err != nil {
			return fmt.Errorf("libav: write subtitle: %w", err)
		}
	}
}
