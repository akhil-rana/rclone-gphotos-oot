# Run on Windows

Open PowerShell in the project folder. If needed, replace the example path with the location where you cloned this repository:

```powershell
Set-Location "C:\path\to\rclone-gphotos-oot"
```

Build the Windows executable (skip this if `rclone.exe` is already there):

```powershell
go build -o rclone.exe rclone.go
```

Check that it starts, then open the interactive remote setup:

```powershell
.\rclone.exe version
.\rclone.exe config
```

In the configuration menu, create a new remote and enter `gphotosmobile` as its type. On Windows, choose the manual authentication option; automated browser login is supported only on Linux desktop.

On the first run, this is normal:

```text
NOTICE: Config file "...\rclone.conf" not found - using defaults
No remotes found, make a new one?
n) New remote
s) Set configuration password
q) Quit config
n/s/q>
```

Type `n` and press Enter. Name the remote (for example, `gphotos`), then select or enter `gphotosmobile` when asked for the storage type. At `auth_data`, choose `2` for **Manual Login** (or type `manual`); option `1` / `auto` fails on Windows. Follow the prompts to retrieve and enter the `oauth_token` from your browser. Enter the token only in your local PowerShell session; do not share it in chat or logs.

At `unlimited_upload`, press Enter to keep the default `false`. Setting it to `true` spoofs a Pixel device and the project warns this may put your Google account at risk. Select the desired `list_mode`, then answer `n` to advanced config unless you specifically need those options.

If a setup attempt using `auto` already created an incomplete remote, remove that entry before retrying (replace `GPhotos-Dev` if you chose another name):

```powershell
.\rclone.exe config delete GPhotos-Dev
.\rclone.exe config
```

The delete command removes only that remote's configuration entry. Then create it again using manual login.

Once configuration is finished, your remote appears under `Current remotes:`. Press `q` to exit the configuration menu:

```text
Current remotes:

Name                 Type
====                 ====
gphotos              gphotosmobile

e) Edit existing remote
n) New remote
d) Delete remote
r) Rename remote
c) Copy remote
s) Set configuration password
q) Quit config
e/n/d/r/c/s/q> q
```

After creating a remote named `gphotos`, run commands from this folder with the `.` prefix:

```powershell
.\rclone.exe ls gphotos:
.\rclone.exe lsd gphotos:
.\rclone.exe copy "C:\Users\YourName\Pictures" gphotos:pictures --progress
```

## Visualize with the Rclone Web GUI

After configuring your remote, you can open Rclone's built-in web interface to browse your Google Photos library and manage transfers visually:

```powershell
.\rclone.exe gui
```

(Or run `rclone gui` if you have already added the binary to your `PATH`.)

Rclone starts a local web server and opens the GUI in your default browser:

```text
2026/10/24 15:09:15 ERROR : internal error: no overview data found for "gphotosmobile"
2026/10/24 15:09:15 NOTICE: Serving remote control on http://127.0.0.1:51615/
2026/10/24 15:09:15 NOTICE: Serving GUI version 1.1.11 on http://127.0.0.1:51614/
2026/10/24 15:09:15 NOTICE: GUI available at http://127.0.0.1:51614/login?pass=<session-token>&url=http%3A%2F%2F127.0.0.1%3A51615%2F&user=gui
2026/10/24 15:09:23 NOTICE: Application Data: Can't follow symlink without -L/--copy-links
2026/10/24 15:09:23 NOTICE: Cookies: Can't follow symlink without -L/--copy-links
2026/10/24 15:09:23 NOTICE: Local Settings: Can't follow symlink without -L/--copy-links
2026/10/24 15:09:23 NOTICE: My Documents: Can't follow symlink without -L/--copy-links
2026/10/24 15:09:23 NOTICE: NetHood: Can't follow symlink without -L/--copy-links
2026/10/24 15:09:23 NOTICE: PrintHood: Can't follow symlink without -L/--copy-links
2026/10/24 15:09:23 NOTICE: Recent: Can't follow symlink without -L/--copy-links
```

If the browser does not open automatically, copy the `GUI available at http://127.0.0.1:...` link from your terminal and open it in your browser. In the web interface, go to **Explorer**, type or select your configured remote (for example, `gphotos:` or `Satya GDrive:`), and browse your files.

The `Can't follow symlink without -L/--copy-links` notices for `Application Data`, `Cookies`, `My Documents`, and similar entries are normal on Windows when the GUI scans your local user profile directory, which contains legacy Windows junction points. Press `Ctrl+C` in PowerShell when you want to stop the GUI server.
