package clip

// Standard resolution tiers, by the height of their 16:9 frame.
var resolutionTiers = []int{240, 360, 480, 576, 720, 1080, 1440, 2160}

// ResolutionClass names a video's resolution by the smallest standard 16:9
// frame it fits in, so 1920x800 (cropped widescreen) and 1440x1080 (4:3)
// are both 1080, and 704x480 is 480. It returns 0 for unknown sizes.
func ResolutionClass(w, h int) int {
	if w <= 0 || h <= 0 {
		return 0
	}
	for _, t := range resolutionTiers {
		// A little slack for odd encodes such as 1920x1088 or 720x486.
		if float64(h) <= float64(t)*1.03 && float64(w) <= float64(t)*16/9*1.03 {
			return t
		}
	}
	return resolutionTiers[len(resolutionTiers)-1]
}

// FitWithin scales w x h down, never up, to fit the 16:9 frame whose height
// is maxHeight, keeping the aspect ratio and even dimensions. A maxHeight of
// 0 or less leaves the size alone (apart from making it even).
func FitWithin(w, h, maxHeight int) (int, int) {
	if maxHeight > 0 && w > 0 && h > 0 {
		scale := 1.0
		if s := float64(maxHeight) / float64(h); s < scale {
			scale = s
		}
		if s := float64(maxHeight) * 16 / 9 / float64(w); s < scale {
			scale = s
		}
		if scale < 1 {
			w = int(float64(w)*scale + 0.5)
			h = int(float64(h)*scale + 0.5)
		}
	}
	return w &^ 1, h &^ 1
}
