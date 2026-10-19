# Licensing notes for the iOS build

The Banana Clip app is closed source and sold on the App Store. It links
FFmpeg, which is LGPL 2.1 or later unless built with GPL components. The
build here is deliberately LGPL-only:

- `third_party/build-ffmpeg.sh` never passes `--enable-gpl` or
  `--enable-nonfree`, and fails if either shows up in the generated config.
  No libx264, libx265, or other GPL-only components are compiled in.
- H.264 encoding uses Apple's VideoToolbox (`h264_videotoolbox`) and AAC uses
  AudioToolbox (`aac_at`). Decoding uses the VideoToolbox hwaccel where the
  device supports it. These are Apple-licensed codecs, which also covers the
  H.264/HEVC/AAC patent question for the app.
- Subtitle rendering links libass (ISC), FreeType (FTL), HarfBuzz (MIT) and
  FriBidi (LGPL 2.1+). HarfBuzz is built with the CoreText backend; no
  fontconfig.
- The Go core (this repository) is MIT. go-astiav is MIT.

Obligations the app carries:

1. Attribution: the Licenses screen in Settings names FFmpeg and the LGPL
   and the other libraries.
2. Source offer: publish the exact FFmpeg and dependency sources plus the
   build scripts (`third_party/`) so anyone can rebuild the libraries.
3. Relinking: LGPL 2.1 §6 requires that users can relink the app against a
   modified library. The libraries are currently linked statically for
   simplicity while the app is in development. Before shipping, package
   FFmpeg as embedded dynamic frameworks (what FFmpegKit did for its LGPL
   builds) so the obligation is met in the conventional way. The Go core is
   a static archive and links against those frameworks; nothing else
   changes.
4. Do not modify FFmpeg itself. If a patch becomes necessary, publish it
   alongside the sources.

This is the standard playbook commercial iOS apps using FFmpeg follow, not
legal advice.
