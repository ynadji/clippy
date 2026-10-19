//go:build libav

package libav

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/asticode/go-astiav"
)

// Thumbnail decodes the first frame at or after t and returns it as a JPEG
// no wider than maxWidth. It is what the app shows as the "screen" of its
// player view and updates as the user scrubs.
func Thumbnail(ctx context.Context, input, headers string, t time.Duration, maxWidth int, hwaccel string) ([]byte, error) {
	src, err := openSource(ctx, input, headers)
	if err != nil {
		return nil, err
	}
	defer src.close()
	vs, _, err := pickStreams(src.fc, -1)
	if err != nil {
		return nil, err
	}
	for _, s := range src.fc.Streams() {
		if s.Index() != vs.Index() {
			s.SetDiscard(astiav.DiscardAll)
		}
	}
	dec, err := openDecoder(src.fc, vs, hwaccel)
	if err != nil {
		return nil, err
	}
	defer dec.close()
	if err := src.seekTo(t); err != nil {
		return nil, fmt.Errorf("libav: seek: %w", err)
	}

	codec := astiav.FindEncoderByName("mjpeg")
	if codec == nil {
		return nil, errors.New("libav: no mjpeg encoder")
	}
	target := tb(t, vs.TimeBase())
	var g *graph
	var jpeg []byte
	defer func() {
		if g != nil {
			g.close()
		}
	}()
	errFound := errors.New("found")

	onOut := func(f *astiav.Frame) error {
		enc := &encoder{name: "mjpeg", cc: astiav.AllocCodecContext(codec), pkt: astiav.AllocPacket()}
		defer enc.close()
		enc.cc.SetWidth(f.Width())
		enc.cc.SetHeight(f.Height())
		enc.cc.SetPixelFormat(astiav.PixelFormatYuvj420P)
		enc.cc.SetTimeBase(astiav.NewRational(1, 25))
		enc.cc.SetQmin(2)
		enc.cc.SetBitRate(int64(f.Width()*f.Height()) * 25 * 3) // ~3 bits/pixel: high quality
		if err := enc.cc.Open(codec, nil); err != nil {
			return fmt.Errorf("libav: open mjpeg: %w", err)
		}
		f.SetPts(0)
		if err := enc.cc.SendFrame(f); err != nil {
			return err
		}
		if err := enc.cc.SendFrame(nil); err != nil && !errors.Is(err, astiav.ErrEof) {
			return err
		}
		if err := enc.cc.ReceivePacket(enc.pkt); err != nil {
			return fmt.Errorf("libav: mjpeg encode: %w", err)
		}
		jpeg = append([]byte(nil), enc.pkt.Data()...)
		enc.pkt.Unref()
		return errFound
	}
	onFrame := func(f *astiav.Frame) error {
		if f.Pts() != astiav.NoPtsValue && f.Pts() < target {
			return nil
		}
		if g == nil {
			w, h := f.Width(), f.Height()
			if maxWidth > 0 && w > maxWidth {
				h = int(float64(h)*float64(maxWidth)/float64(w) + 0.5)
				w = maxWidth
			}
			w, h = w&^1, h&^1
			spec := fmt.Sprintf("scale=%d:%d,format=yuvj420p", w, h)
			var gerr error
			g, gerr = newGraph(false, func(p *astiav.BuffersrcFilterContextParameters) {
				p.SetWidth(f.Width())
				p.SetHeight(f.Height())
				p.SetPixelFormat(f.PixelFormat())
				p.SetSampleAspectRatio(f.SampleAspectRatio())
				p.SetTimeBase(vs.TimeBase())
			}, spec)
			if gerr != nil {
				return gerr
			}
		}
		return g.push(f, onOut)
	}

	pkt := astiav.AllocPacket()
	defer pkt.Free()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := src.fc.ReadFrame(pkt); err != nil {
			if errors.Is(err, astiav.ErrEof) {
				break
			}
			return nil, err
		}
		if pkt.StreamIndex() != vs.Index() {
			pkt.Unref()
			continue
		}
		err := dec.decode(pkt, onFrame)
		pkt.Unref()
		if errors.Is(err, errFound) {
			return jpeg, nil
		}
		if err != nil {
			return nil, err
		}
	}
	if err := dec.decode(nil, onFrame); errors.Is(err, errFound) {
		return jpeg, nil
	} else if err != nil {
		return nil, err
	}
	return nil, errors.New("libav: no frame at that position")
}
