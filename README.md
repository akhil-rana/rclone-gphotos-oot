# Rclone Google Photos Out-of-Tree Remote

An out-of-tree Rclone remote for the Google Photos mobile API. It provides a configurable way to backup photos using unofficial endpoints.

## Features

- **Unlimited Free Uploads:** Spoofs the Google Pixel 1 XL device to enable unlimited original quality photo backups (configured via the `unlimited_upload` flag).
- **Listing Modes:** Supports multiple list layouts such as `flat`, `year`, `year_month`, and `calendar` to organize the library natively without extra requests (`list_mode` config).
- **Fast Local Indexing:** Creates an offline SQLite cache of the user's Google Photos library to prevent excessive API requests.
- **Sync Integration:** Inherits all the latest features, cross-platform compatibility, and sync mechanisms from the core Rclone project.

## Configuration

Configure the remote in your `rclone.conf`:

```ini
[my_gphotos]
type = gphotosmobile
auth_data = <YOUR_ANDROID_ID_AUTH_STRING>
unlimited_upload = true
list_mode = year_month
```

## Build Instructions

To build the custom rclone binary with the backend enabled:

```sh
go build -o rclone-gphotos rclone.go
```
