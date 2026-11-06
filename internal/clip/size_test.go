package clip

import "testing"

func TestResolutionClass(t *testing.T) {
	cases := []struct{ w, h, want int }{
		{704, 480, 480}, {720, 480, 480}, {720, 486, 480}, {720, 576, 576}, {640, 360, 360},
		{1280, 720, 720}, {1280, 534, 720}, {960, 720, 720},
		{1920, 1080, 1080}, {1920, 800, 1080}, {1440, 1080, 1080}, {1920, 1088, 1080},
		{3840, 2160, 2160}, {3840, 1600, 2160}, {2560, 1440, 1440}, {7680, 4320, 2160},
		{0, 0, 0},
	}
	for _, c := range cases {
		if got := ResolutionClass(c.w, c.h); got != c.want {
			t.Errorf("ResolutionClass(%d, %d) = %d, want %d", c.w, c.h, got, c.want)
		}
	}
}

func TestFitWithin(t *testing.T) {
	cases := []struct{ w, h, max, ww, wh int }{
		{1920, 1080, 720, 1280, 720},
		{1920, 800, 720, 1280, 532}, // cropped widescreen is limited by width
		{1440, 1080, 720, 960, 720}, // 4:3 is limited by height
		{3840, 2160, 1080, 1920, 1080},
		{704, 480, 1080, 704, 480}, // never upscale
		{1280, 720, 0, 1280, 720},  // 0 keeps the source
		{1281, 721, 0, 1280, 720},  // but dimensions are made even
	}
	for _, c := range cases {
		if w, h := FitWithin(c.w, c.h, c.max); w != c.ww || h != c.wh {
			t.Errorf("FitWithin(%d, %d, %d) = %dx%d, want %dx%d", c.w, c.h, c.max, w, h, c.ww, c.wh)
		}
	}
}
