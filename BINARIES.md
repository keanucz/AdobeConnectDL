# Embedded Binaries

AdobeConnectDL embeds pre-compiled binaries for MP4Box (subtitle embedding). These are compressed with 7z ultra compression to minimize binary size.

## Compression Command

These are the compression settings used when compressing the MP4Box binaries:

```bash
7z a -t7z -mx=9 -mfb=273 -ms -md=31 output.7z input_binary
```

## MP4Box Binaries

| Platform | Source |
|----------|--------|
| macOS ARM64 | Self-compiled from [GPAC source](https://github.com/gpac/gpac) |
| macOS Intel | Self-compiled from [GPAC source](https://github.com/gpac/gpac) |
| Windows AMD64 | Self-compiled from [GPAC source](https://github.com/gpac/gpac) |
| Linux AMD64 | Self-compiled from [GPAC source](https://github.com/gpac/gpac) |

GPAC/MP4Box is licensed under LGPL v2.1: https://gpac.io/about-gpac/

aria2 is licensed under GPL v2: https://aria2.github.io/
