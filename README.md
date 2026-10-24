# Rclone Google Photos (Mobile API Remote)

An out-of-tree Rclone plugin for Google Photos that uses Google's internal Mobile API endpoints rather than the restricted public REST API.

> [!WARNING]
> **Use at your own risk.** This project interacts with internal, reverse-engineered Google Photos mobile APIs and is not endorsed or supported by Google. Using unofficial APIs and spoofing hardware identifiers violates Google's Terms of Service and could potentially result in your Google account being flagged, suspended, or terminated. The developers and contributors are not responsible for any consequences resulting from the use of this software.

---

## Overview

### What is this?
Google's official public API for Google Photos is heavily crippled:
- **Restricted Access:** An application can only access files that were uploaded by that specific application itself (`photoslibrary.readonly.appcreateddata`). It cannot access your pre-existing Google Photos library or photos uploaded from your mobile phone.
- **Forced Compression & Resolution Caps:** Media uploaded or downloaded through the public API is compressed or resolution-capped (16MP for photos, 1080p for videos), stripping original quality and metadata.
- **Strict Quotas:** The public API enforces restrictive rate limits and daily quotas.

This out-of-tree Rclone plugin emulates the official Google Photos Android app via gRPC/Protobuf protocols. It provides access to your **entire** library with bit-for-bit full-resolution uploads and downloads with original EXIF metadata intact, giving you true cloud backup capabilities for Google Photos.

---

## Features

- **Full-Resolution Original Uploads & Downloads:** Upload and download media in bit-for-bit untouched quality with complete EXIF metadata intact. Bypasses the 16MP / 1080p compression caps of the public API.
- **Deduplication:** Checks file hashes before transferring to avoid duplicate uploads and save bandwidth.
- **Local Metadata Cache:** Offline metadata caching enables instant operations (`ls`, `lsd`, `tree`) without repeatedly querying Google endpoints or triggering rate limits.
- **Virtual Listing Modes:** Support for different library layouts, including flat, hierarchical calendar date trees, or virtual directory structures. See [Listing Modes](#listing-modes) for more details.
- **Automated In-Memory Browser Login (Linux Desktop):** Fast, zero-hassle authentication. Launches Chromium, Chrome, or Brave in debug mode and captures session tokens directly from browser memory via Chrome DevTools Protocol (CDP).
- **Manual Authentication Option:** Simple manual token entry (`oauth_token`) for headless servers, macOS, Windows, or alternative environments.
- **Arbitrary Binary Data Storage (`support_binary_data`):** Store non-media files (archives, documents, binaries up to 50 GB) by converting raw bytes into lossless RGB24 media (PNG images for files < 10 MB, uncompressed MKV video for files up to 50 GB). Metadata (original filename, exact byte size, and SHA-1 hash) is stored in the caption JSON for instant verification with `rclone check`.
- **Unlimited Original Quality Uploads (Pixel Spoofing):** Emulates a Google Pixel 1 XL device during upload, allowing original quality uploads without consuming your Google storage quota.
  > **Use at your own risk:** Spoofing device signatures violates Google's Terms of Service. We are not responsible if Google restricts or bans your account for spoofing hardware identifiers.

---

## Why Not Part of the Native Rclone Repository?

Because this backend relies on reverse-engineered internal Google Photos mobile endpoints, Protobuf protocols, and device impersonation, it cannot be merged into mainline Rclone.

An upstream pull request was proposed in [rclone/rclone#9135](https://github.com/rclone/rclone/pull/9135), where **Nick Craig-Wood ([@ncw](https://github.com/ncw))**, the lead maintainer of Rclone, [formally declined](https://github.com/rclone/rclone/pull/9135#issuecomment-4449581121) merging it into core Rclone due to significant project and legal risks. An out-of-tree backend was recommended in that discussion, hence this implementation.

---

## Setup & Configuration

### 1. Get the Binary

Choose one of the following methods to obtain the Rclone executable with the out-of-tree Google Photos backend:

#### Option A: Download Pre-compiled Binary (Recommended)
Download the latest pre-compiled release for your operating system and architecture from the **[Releases](https://github.com/akhil-rana/rclone-gphotos-oot/releases)** page:
- **Linux:** `.tar.gz` binary archive, `.deb` package, or `.rpm` package.
- **macOS:** `.zip` archive (`osx`).
- **Windows:** `.zip` archive (`windows`).

Extract the executable (or install the package) and proceed directly to **Step 2**.

#### Option B: Build from Source
If you have Go installed and prefer to build the binary yourself, clone the repository and run:

```bash
go build -o rclone rclone.go
```

---

### 2. Configure a New Remote
Launch Rclone's interactive configuration menu:

```bash
rclone config
```

1. Enter `n` for a new remote and name it (e.g., `gphotos`).
2. Select `Google Photos (Mobile API - full access)` (type `gphotosmobile`).
3. Choose your authentication method for `auth_data`:
   - **`1 / Automated Browser Login` (Linux Desktop):** Launches an automated Chromium, Chrome, or Brave window. Sign in to your Google Account. Once completed, the window closes automatically, and credentials are saved.
   - **`2 / Manual Login`:** Visit `https://accounts.google.com/EmbeddedSetup` in any browser with Developer Tools open, sign in, copy the `oauth_token` cookie (starts with `oauth2_4/`), and paste it into the prompt.
4. Set `unlimited_upload`:
   - Set to `true` to spoof Pixel 1 XL for quota-free uploads *(use with caution)*.
   - Set to `false` (default) for standard uploads against your Google account quota.
5. Set `list_mode`:
   - Select your preferred listing layout (see [Listing Modes](#listing-modes)).

---

## Listing Modes (`list_mode`)

You can control how Rclone organizes and displays your photo library using the `list_mode` option:

| Mode | Structure | Description |
| :--- | :--- | :--- |
| `flat` | `filename.jpg` | All photos and videos appear in the root directory. Duplicate filenames automatically have deduplication suffixes appended. |
| `calendar` | `YYYY/YYYY-MM/YYYY-MM-DD/filename.jpg` | Hierarchical date tree based on the photo's original capture timestamp (EXIF data). Aliased to `day` or `week`. |
| `month` | `YYYY/YYYY-MM/filename.jpg` | Grouped into two-tier year and month directories (`YYYY/YYYY-MM/`). Aliased to `year_month`. |
| `year` | `YYYY/filename.jpg` | Grouped strictly into single-tier year directories (`YYYY/`). |
| `description_path` | `folder/subfolder/filename.jpg` | **BETA.** Full virtual filesystem tree backed by Google Photos captions. |

### Description-Based Virtual Paths (`description_path`) [BETA]

> [!NOTE]
> This mode is experimental. It is not recommended if you rely on the description field for personal notes or captions, as Rclone manages path tags in this field. However, for power users who want traditional folder structures on Google Photos, it provides true directory management.

Google Photos does not support true folders natively. In `description_path` mode, Rclone embeds virtual directory paths into the caption/description of the file itself:

```text
{paths:["/pictures/vacation", "/favorites"]}
```

#### How it works:
- **Uploads & Copies:** When you copy a file into a folder (e.g., `rclone copy photo.jpg gphotos:pictures/vacation`), Rclone automatically appends `{paths:["/pictures/vacation"]}` to the caption.
- **Multiple Virtual Folders (Hardlinks):** If you copy the same file into a second folder (`gphotos:pictures/favorites`), Rclone avoids re-uploading the bytes; instead, it appends the new path to the JSON array: `{paths:["/pictures/vacation", "/pictures/favorites"]}`. The file instantly appears in both folders!
- **Safe Unlinking on Delete:** When deleting a file from one virtual folder, Rclone only strips that folder from the caption. The actual image is only moved to the Google Photos Trash when its last remaining virtual path is removed.

---

## Arbitrary Binary Storage (`support_binary_data`) [BETA]

> [!WARNING]
> **Beta Feature:** Arbitrary binary data storage via RGB24 media containers is currently in **Beta**. While fully lossless and verified with SHA-1 integrity checks, use with caution for critical production workloads.

When the advanced option `support_binary_data = true` is enabled, the backend allows uploading **any arbitrary file type** (such as `.zip`, `.iso`, `.tar.gz`, documents, or executables) by converting raw bytes into lossless RGB24 visual media:

- **Small Files (Below `min_size_needed_for_binary_video`):** Encoded as lossless uncompressed PNG images (minimum 240×240 resolution, up to 100 Megapixels). Dimensions are dynamically calculated to minimize padding waste to near zero.
- **Large Files (Up to 50 GB):** Encoded as lossless uncompressed MKV videos (`V_MS/VFW/FOURCC` RGB24).
- **`min_size_needed_for_binary_video`:** (Advanced) Threshold to transition from PNG image to MKV video. Allowed range: **5M to 50M** (default: **10M**). Files smaller than this threshold are stored as lossless uncompressed PNG images; larger files are stored as MKV videos.
- **`binary_video_resolution`:** (Advanced) Target video resolution for binary MKV containers. Options: `auto` (default), `240p`, `360p`, `480p`, `720p`, `1080p`, `1440p`, `4K`, `4096p`.
  - **1-Second Safeguard:** Google Photos requires video containers to have a valid duration ($\ge 1\text{ second}$). In `auto` mode, Rclone automatically selects the highest resolution that guarantees at least 1 second of video and minimizes padding.
  - **Validation:** If a fixed resolution is chosen, Rclone validates that your `binary_data_chunk_size` is large enough to sustain at least 1 second of video at 24fps (for example, selecting `4K` with a `64M` chunk size is rejected during configuration because 4K requires ~570MB for 1 second).
- **Maximum File Size:** Supports files up to **50 GB**.
- **Force Binary Encoding for Media (`encode_all_as_binary`):** By default, native media files (`.jpg`, `.mp4`, etc.) are uploaded directly without binary transformation. Setting the advanced flag `encode_all_as_binary = true` forces even native photos and videos to be encoded into uncompressed binary media containers with `binaryDetails` metadata.
- **Embedded Metadata:** The original filename, exact unpadded byte size, and full SHA-1 hash are recorded in the item's caption JSON:
  ```json
  {
    "paths": ["/Backups/data.tar.gz"],
    "binaryDetails": {
      "name": "data.tar.gz",
      "size": 104857600,
      "sha1": "2fd4e1c67a2d28fced849ee1bb76e7391b93eb12"
    }
  }
  ```
- **Transparent Recovery:** When listing (`rclone ls`), downloading (`rclone copy`), or verifying (`rclone check`), Rclone automatically displays the original filename, reports the exact original size, verifies the SHA-1 hash, and discards trailing padding bytes on the fly.

### Binary Data Chunking & Virtual Seeking (`binary_data_chunking`)

Because Google Photos' media download endpoints do not support HTTP `Range` requests, seeking within large files normally requires streaming sequentially from byte 0. 

To solve this, you can enable **Binary Data Chunking**:
- **`binary_data_chunking = true`:** Splits large binary files into individual media chunks upon upload (e.g. `data.iso.part000.mkv`, `data.iso.part001.mkv`).
- **`binary_data_chunk_size`:** Sets the chunk size. Allowed range: **32M to 512M** (recommended: **64M**).
- **`binary_data_chunk_concurrency`:** Number of chunks uploaded concurrently in parallel. Allowed range: **1 to 16** (default: **4**). Maximizes throughput on high-speed internet connections.
- **Configuration Validation:** Rclone strictly validates that `support_binary_data = true` is enabled before allowing chunking to be activated. If disabled, or if `binary_data_chunk_size` is outside 32M–512M or concurrency is outside 1–16, Rclone immediately refuses the configuration.
- **Virtual HTTP Range Seeking:** When an application (like `mpv`, VLC, or `rclone cat --offset`) sends an HTTP `Range` request for offset `X`, Rclone calculates the exact chunk index (`X / chunkSize`) and downloads **only that specific chunk**, achieving instant seeking without fetching preceding gigabytes of data.
- **Transparent Composite Listing:** All chunks are displayed in `rclone ls` as a single, unified file with full original size and SHA-1 hash.

### Timestamp Override (`upload_timestamp`)

By default, Google Photos indexes newly uploaded items with the current date and time, placing them at the very top of your photos timeline. When storing large backups or chunked binary files, this can clutter your personal photo stream.

You can override the capture timestamp using the advanced option `upload_timestamp`:
- **`upload_timestamp = "oldest"` (or `"1906-01-01"`):** Sets the capture timestamp to **January 1, 1906 00:00:00 UTC** (Unix timestamp `-2019686400`), which is the **oldest allowable date supported by Google Photos**. This sends uploaded files and chunks over 120 years in the past to the ultimate bottom of your Google Photos timeline, ensuring your recent feed stays pristine!
- **`upload_timestamp = "epoch"` (or `"1970-01-01"`):** Sets the capture timestamp to **January 1, 1970 00:00:00 UTC** (Unix timestamp `0`).
- **`upload_timestamp = "file"`:** Automatically uses the source file's local modification time.
- **`upload_timestamp = "YYYY-MM-DD"` (or Unix timestamp integer):** Sets a specific custom date (e.g. `'2000-01-01'` or `'0'`). Dates earlier than 1906-01-01 are automatically rejected.

> [!NOTE]
> **Zero File Modification:** This setting *only* controls the timeline sorting index on the Google Photos portal via the commit request. The actual uploaded file bytes, internal EXIF metadata, video container streams, and restored SHA-1 checksums remain 100% original, bit-for-bit identical, and untouched.

### Scoped Album Mode (`album_ids`)

Google Photos enforces a hard limit of **20,000 items per album**. You can configure a remote scoped to one or more albums with automatic capacity tracking and spillover:

- **`album_ids`:** A comma-separated list of Google Photos Album IDs (e.g. `AF1QipAlbum1,AF1QipAlbum2,AF1QipAlbum3`).
  - Leave empty to access your entire library.
  - To find an Album ID, open the album in your web browser at [photos.google.com](https://photos.google.com) and copy the ID from the URL: `https://photos.google.com/album/<ALBUM_ID>`.
- **`max_items_per_album`:** (Advanced) Maximum number of items allowed per album before spilling over to the next album in `album_ids` (default: `20000`).
- **Unified Listing:** When configured, `rclone ls` aggregates and displays items from **all** listed albums transparently. Items in your library outside these albums are completely hidden.
- **Sequential Spillover on Upload:** Uploads (including chunked binary containers) fill the first album in `album_ids` up to `max_items_per_album`. When an album is full, uploads automatically spill over to the second album, then the third, and so on.
- **Capacity Safety:** If all configured albums have reached the item limit, uploads fail with a clear error prompt instructing you to add a new album ID to continue.

---

## Usage Examples

> [!NOTE]
> **Initial Library Caching:** When you execute any operation (such as `rclone ls`) for the first time on a remote, Rclone performs an initial library sync to build your local metadata cache. For accounts containing thousands or tens of thousands of photos, this initial indexing step may take several minutes or more depending on library size and network speed. Once complete, all subsequent operations are near-instantaneous as Rclone only pulls quick incremental delta updates.
>
> <details>
> <summary><b>Harmless Notice: <code>internal error: no overview data found</code></b> (Click to expand)</summary>
>
> You will notice the following message printed when running commands:
> ```text
> ERROR : internal error: no overview data found for "gphotosmobile"
> ```
> **This message is completely cosmetic and safe to ignore.**
>
> Upstream Rclone's internal registry expects embedded overview documentation metadata that is only generated during official in-tree Rclone builds. Because this backend operates as an independent out-of-tree plugin, this notice is emitted by Rclone's core registry and cannot be suppressed without modifying upstream Rclone. It has zero impact on functionality, reliability, or file transfers.
> </details>

### List Files
```bash
# List all files according to your configured list_mode
rclone ls gphotos:

# List subdirectories (e.g., in calendar or description_path mode)
rclone lsd gphotos:
```

### Upload / Backup Files
```bash
# Upload a local directory
rclone copy /home/user/Pictures gphotos:pictures --progress

# Sync a local directory to Google Photos
rclone sync /home/user/Pictures gphotos:pictures --progress
```

### Download Files in Original Quality
```bash
# Download a folder preserving full, bit-for-bit original quality
rclone copy gphotos:pictures /home/user/RestoredPhotos --progress
```

---

## Acknowledgements & References

This project builds upon the foundational work of the open-source community:

- **[@xob0t](https://github.com/xob0t):** Author of the original [rclone/rclone#9135](https://github.com/rclone/rclone/pull/9135) pull request. A significant portion of the core Mobile API reverse-engineering and backend logic was adapted from his fork branch [`xob0t:backend/gphotosmobile`](https://github.com/xob0t/rclone/tree/backend/gphotosmobile).
- **[gotohp](https://github.com/xob0t/gotohp):** Also created by `@xob0t`, serving as an invaluable reference for reverse-engineering Google Photos internal Protobuf schemas and mobile upload pipelines.
- **[rclone_out_of_tree_example](https://github.com/rclone/rclone_out_of_tree_example):** Provided the foundational architectural template and build scaffolding for structuring an independent out-of-tree remote backend.

---

> [!NOTE]
> **Development Disclaimer:** This project is **AI-assisted, not "vibe coded"**. While the implementation has been heavily and professionally reviewed, it has **not** been professionally or exhaustively tested. Because the code was written with AI and interacts with undocumented APIs, please use it with caution.
