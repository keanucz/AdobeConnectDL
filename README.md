<p align="center">
  <img src="./pictures/logo.svg" alt="AdobeConnectDL logo" height="80" />
</p>

<h1 align="center">AdobeConnectDL</h1>

<p align="center">
  <a href="https://github.com/keanucz/AdobeConnectDL/releases"><img src="https://img.shields.io/github/v/release/keanucz/AdobeConnectDL?style=flat-square" alt="GitHub release"></a>
  <a href="https://github.com/keanucz/AdobeConnectDL/actions/workflows/build-and-release.yaml"><img src="https://github.com/keanucz/AdobeConnectDL/actions/workflows/build-and-release.yaml/badge.svg" alt="Build"></a>
  <a href="https://github.com/keanucz/AdobeConnectDL/actions/workflows/test.yaml"><img src="https://github.com/keanucz/AdobeConnectDL/actions/workflows/test.yaml/badge.svg" alt="Test"></a>
  <a href="https://github.com/keanucz/AdobeConnectDL/actions/workflows/lint.yaml"><img src="https://github.com/keanucz/AdobeConnectDL/actions/workflows/lint.yaml/badge.svg" alt="Lint"></a>
  <a href="./LICENSE"><img src="https://img.shields.io/github/license/keanucz/AdobeConnectDL?style=flat-square" alt="License"></a>
</p>

This is a Go CLI (using Cobra) that automatically grabs the MP4 and VTT URLs from an Adobe Connect recording page, downloads them, merges everything together, and also spits out a nice `.txt` transcript and other metadata alongside the recording.

I wrote this because my university uses Adobe Connect to host lectures and store recordings, and for some reason you can only view recordings online, not download them. Given that I'm sometimes in places with no Internet access or terrible connectivity, I built this tool to solve my own (and my fellow students') woes so we can watch our lectures anywhere, any time.

## ✨ What it does

For each Adobe Connect recording URL you give it, AdobeConnectDL will:

- 🔻 Locate the backing MP4 and subtitle (VTT) URLs from the recording page
- 📥 Download the recording and subtitles
- 🎛️ Embed subtitles into the video
- 🗂️ Extract and save:
  - Transcript (`transcript.txt`)
  - Chat log (`chat_log.txt`)
  - Captions (`captions.vtt`)
  - Attached documents (plus a `documents.txt` index)
  - Metadata (`metadata.json`)
- 📁 Put everything into a neatly named directory for that recording

## 🚀 Usage

1. **Download the binary**

   Grab the binary from the releases page for your platform. ⬇️  

   I've embedded a copy of `MP4Box` for most platforms, so you shouldn't need to install any external dependencies.

   > **macOS users:** See [Running on macOS](#-running-on-macos-unsigned-binary) below for instructions on running unsigned binaries.

2. **Open the recordings list**

   For any lecture you want to download, go to the Adobe Connect recordings page (pictured below):

   ![Screenshot of meeting recordings list page](./pictures/screenshot1.png)

3. **CRUCIAL STEP: open the specific recording**

   Click on the **recording** you want to download.

   In your browser’s URL bar you should see something like:

   ```text
   https://your-domain.adobeconnect.com/recording-id/?session=YOUR_SESSION_TOKEN
   ```

   This is the URL you need to pass to `adobeconnectdl`.  
   (If you use the generic list page URL instead, the tool won’t work.)

   ![Screenshot of lecture recording page](./pictures/screenshot2.png)

4. **Run `adobeconnectdl`**

   Feed the recording URL into the CLI and let it do its thing:

   ```bash
   ❯ ./adobeconnectdl download "https://your-domain.adobeconnect.com/recording-id/?session=YOUR_SESSION_TOKEN"
   INFO Starting batch download count=1
   INFO MP4Box located path=/usr/local/bin/MP4Box
   INFO Processing recording 1/1 url="https://your-domain.adobeconnect.com/recording-id/?session=YOUR_SESSION_TOKEN"
   INFO Downloading video progress=5% downloaded="16.4 MB" total="328.0 MB"
   INFO Downloading video progress=10% downloaded="32.8 MB" total="328.0 MB"
   INFO Downloading video progress=15% downloaded="49.2 MB" total="328.0 MB"
   INFO Downloading video progress=20% downloaded="65.6 MB" total="328.0 MB"
   INFO Downloading video progress=25% downloaded="82.0 MB" total="328.0 MB"
   INFO Downloading video progress=30% downloaded="98.4 MB" total="328.0 MB"
   INFO Downloading video progress=35% downloaded="114.8 MB" total="328.0 MB"
   INFO Downloading video progress=40% downloaded="131.2 MB" total="328.0 MB"
   INFO Downloading video progress=45% downloaded="147.6 MB" total="328.0 MB"
   INFO Downloading video progress=50% downloaded="164.0 MB" total="328.0 MB"
   INFO Downloading video progress=55% downloaded="180.4 MB" total="328.0 MB"
   INFO Downloading video progress=60% downloaded="196.8 MB" total="328.0 MB"
   INFO Downloading video progress=65% downloaded="213.2 MB" total="328.0 MB"
   INFO Downloading video progress=70% downloaded="229.6 MB" total="328.0 MB"
   INFO Downloading video progress=75% downloaded="246.0 MB" total="328.0 MB"
   INFO Downloading video progress=80% downloaded="262.4 MB" total="328.0 MB"
   INFO Downloading video progress=85% downloaded="278.8 MB" total="328.0 MB"
   INFO Downloading video progress=90% downloaded="295.2 MB" total="328.0 MB"
   INFO Downloading video progress=95% downloaded="311.6 MB" total="328.0 MB"
   INFO Downloading video progress=100% downloaded="328.0 MB" total="328.0 MB"
   INFO Download complete title="Introduction to Software Engineering - Lecture 12" location="Introduction to Software Engineering - Lecture 12"
   INFO Video saved path="Introduction to Software Engineering - Lecture 12/recording.mp4"

   ✓ Saved recording "Introduction to Software Engineering - Lecture 12" to Introduction to Software Engineering - Lecture 12

   ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
   Download Summary: 1 successful, 0 failed

   ❯ ls Introduction\ to\ Software\ Engineering\ -\ Lecture\ 12
   captions.vtt  chat_log.txt  documents  documents.txt  metadata.json  raw  raw.zip  recording.mp4  transcript.txt
   ```

## 📦 Outputs at a glance

For each recording, you'll typically get a directory like:

- 🎥 `recording.mp4` – the final MP4 with subtitles baked in
- 💬 `captions.vtt` – raw subtitles
- 📝 `transcript.txt` – plain-text transcript
- 🗨️ `chat_log.txt` – chat window contents with names & timestamps
- 📄 `documents/` – any attached documents from the session
- 📑 `documents.txt` – quick index of attached documents
- 🧾 `metadata.json` – assorted recording metadata
- 🔍 `raw.zip` / `raw/` – original Adobe Connect assets (FLV/XML etc.), if you want to poke at them

## 🍎 Running on macOS (unsigned binary)

With Apple Silicon, macOS became much stricter about running unsigned binaries. Since AdobeConnectDL is not signed & notarised with an Apple Developer certificate, macOS will block execution by default.

If you try to run the binary, you'll see an error like:

<p align="center">
  <img src="https://donatstudios.com/assets/86/warning.avif" alt="macOS warning: Apple could not verify binary is free of malware" width="400" />
</p>

> Apple could not verify "adobeconnectdl" is free of malware that may harm your Mac or compromise your privacy

Instead of offering any help, macOS simply offers to move the binary to the trash. Very frustrating!

### Option 1: Add Terminal as a Developer Tool (Recommended)

The cleanest solution is to add Terminal (or your favourite TTY) as a system Developer Tool. This allows it to run any unsigned binary without issues.

**Step 1:** Open **System Settings** and search for "**developer**". Click **Allow applications to use developer tools** in the sidebar. If Terminal is not listed, click the `+` button:

<p align="center">
  <img src="https://donatstudios.com/assets/86/add-terminal.avif" alt="System Settings showing Allow applications to use developer tools with plus button" width="600" />
</p>

**Step 2:** Search for `Terminal` in the file dialog and select it:

<p align="center">
  <img src="https://donatstudios.com/assets/86/search.avif" alt="File chooser searching for Terminal" width="500" />
</p>

**Step 3:** Ensure the toggle next to Terminal is **enabled**:

<p align="center">
  <img src="https://donatstudios.com/assets/86/enable-terminal.avif" alt="Toggle to enable Terminal in developer tools" width="600" />
</p>

**Step 4:** **Restart Terminal** and everything should now work:

<p align="center">
  <img src="https://donatstudios.com/assets/86/success.avif" alt="Terminal showing successful execution" width="500" />
</p>

You can now run any unsigned binary from Terminal without issues!

> *Screenshots & text courtesy of [Jesse Donat](https://donatstudios.com/mac-terminal-run-unsigned-binaries) (CC BY-SA 3.0)*

### Option 2: Remove Quarantine Attribute

If you prefer not to enable developer tools globally, you can remove the quarantine attribute from the binary:

```bash
# Remove quarantine from the binary
xattr -dr com.apple.quarantine /path/to/adobeconnectdl

# Optional: self-sign the binary
codesign -s - --deep --force /path/to/adobeconnectdl
```

### Option 3: Right-Click Open (GUI approach)

1. Go to **Finder > Applications** (or wherever you placed the binary)
2. **Right-click** on `adobeconnectdl` and choose **Open** from the context menu
3. In the dialog, click **Open**
4. On macOS 15 (Sequoia) and above, you may also need to go to **System Settings > Privacy & Security** and click **Open Anyway**

After any of these steps, the binary should work normally.

## ⚡ Download Options

AdobeConnectDL uses Go's native HTTP client with **concurrent downloads** enabled by default for optimal performance. I ran some benchmarks and apparently 12 concurrent workers achieve the best throughput irregardless of network speed. (I also tried testing aria2/curl/wget and the difference between all of them was marginal, with aria2 coming close to native performance).

### Concurrent vs Sequential Downloads

By default, AdobeConnectDL downloads the MP4, ZIP, and all documents concurrently using 12 workers:

```bash
# Concurrent download (default, fastest)
adobeconnectdl download "https://..."

# Sequential download (one file at a time)
adobeconnectdl download --sequential "https://..."
```

### Overwrite Existing Files

By default, the tool will prompt before overwriting existing directories. Use `-y` to skip the prompt:

```bash
# Overwrite without prompting
adobeconnectdl download -y "https://..."
```

### Download Statistics

Add `--stats` to see detailed download timing and speed metrics:

```bash
adobeconnectdl download --stats "https://..."

# Output includes:
# 📊 Download Statistics:
#   Mode: concurrent
#   Total batch time: 45.2s
#   Total data: 328.5 MB
#   Average speed: 7.27 MB/s
```

Use `--stats -v` for per-recording breakdowns when downloading multiple recordings.

## 🧠 Technical details (under the hood)

There are basically two ways to download Adobe Connect recordings:

1. **Direct MP4 & VTT extraction (what AdobeConnectDL uses)**  
   Parse the lecture recording HTML/JavaScript, pull out the MP4 and VTT URLs, and download them directly.

2. **“Raw assets” ZIP + reconstruction (the painful way)**  
   Download a ZIP file containing all the original Flash Video (FLV) chunks and XML files, then try to reconstruct the timeline from that.

I originally explored option 2 (and so did Codex). It turns out that trying to combine several randomly ordered FLV files into a single mp4 is... not fun.

However, that ZIP **does** contain some useful extra data: the transcript with unanonymised names and timestamps for the chat window, plus various session metadata and attachments. That’s why the tool still downloads and keeps those raw assets, as they’re handy for preserving captions, chat logs, transcripts, attached documents, and so on.

Since Adobe Connect already provides an MP4 stored in S3 anyway, I found it far easier to:

- Use the MP4 as the canonical recording
- Pull subtitles, chat logs, transcripts, and documents from the raw assets
- Embed the subtitles into the MP4

This gives you a single, portable video file plus all the sidecar data you might want for later processing or archival.
