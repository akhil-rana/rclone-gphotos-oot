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
- **Unlimited Original Quality Uploads (Pixel Spoofing):**
  > [!WARNING]
  > **Extreme Caution:** This feature emulates a Google Pixel 1 XL (Android 28) device during upload commits, allowing original quality uploads without consuming your Google One storage quota. **Use this entirely at your own risk.** We are not responsible if Google restricts or bans your account for spoofing device signatures.

---

## Why Not Part of the Native Rclone Repository?

Because this backend relies on reverse-engineered internal Google Photos mobile endpoints, Protobuf protocols, and device impersonation, it cannot be merged into mainline Rclone.

An upstream pull request was proposed in [rclone/rclone#9135](https://github.com/rclone/rclone/pull/9135), where **Nick Craig-Wood ([@ncw](https://github.com/ncw))**, the lead maintainer of Rclone, [formally declined](https://github.com/rclone/rclone/pull/9135#issuecomment-4449581121) merging it into core Rclone due to significant project and legal risks. An out-of-tree backend was recommended in that discussion, hence this implementation.

---

## Setup & Configuration

### 1. Build the Binary
Clone the repository and build the custom Rclone executable:

```bash
go build -o rclone rclone.go
```

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

### Description-Based Virtual Paths (`description_path`) — **BETA**

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

## Usage Examples

> [!NOTE]
> **Initial Library Caching:** When you execute any operation (such as `rclone ls`) for the first time on a remote, Rclone performs an initial library sync to build your local metadata cache. For accounts containing thousands or tens of thousands of photos, this initial indexing step may take several minutes or more depending on library size and network speed. Once complete, all subsequent operations are near-instantaneous as Rclone only pulls quick incremental delta updates.

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
