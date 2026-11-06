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
   build scripts (`third_party/`) so anyone can rebuild the libraries. The
   versions are pinned in those scripts (FFmpeg 9.0.2, FriBidi 1.0.16, libass
   0.17.4, FreeType 2.13.3, HarfBuzz 11.2.1), all unmodified upstream
   releases. This repository must be publicly readable for the offer to hold.
3. Relinking: LGPL 2.1 §6 requires that users can relink the app against a
   modified library. FFmpeg and FriBidi, the two LGPL components, are built
   as shared libraries and embedded in the app as dynamic frameworks
   (`third_party/make-xcframeworks.sh`): separate, individually signed
   binaries in the bundle's Frameworks folder, loaded at launch, not linked
   into the app's executable. The Go core is a static archive that calls into
   them. Anyone can rebuild the frameworks from the published scripts and
   substitute them. libass, FreeType and HarfBuzz are permissively licensed
   and are linked statically into libavfilter.

   App Store code signing means a substituted framework will not load on a
   stock device without re-signing the app. That limitation applies to every
   iOS app using LGPL code; dynamic linking plus a source offer is the
   accepted good-faith way to meet the obligation, and what FFmpegKit's LGPL
   builds did.
4. Do not modify FFmpeg itself. If a patch becomes necessary, publish it
   alongside the sources.

This is the standard playbook commercial iOS apps using FFmpeg follow, not
legal advice.
