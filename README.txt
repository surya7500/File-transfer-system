NEARBY PRO — PORTABLE WINDOWS VERSION
====================================

This edition is a single Windows 64-bit executable. It does NOT require:
- Python
- Flask
- Java / JDK
- Node.js
- .NET runtime
- Any separate app or runtime installation

START
1. Extract Nearby_Standalone_Windows.zip to a folder you can write to (for example Desktop\Nearby).
2. Double-click NearbyPro.exe.
3. Windows may ask whether to allow it through Defender Firewall. Allow access on
   Private networks only.
4. The terminal window displays a 10-digit sharing PIN and one or more PC LAN addresses.
5. Open http://127.0.0.1:5000 on the PC.
6. On your phone, connect to the same Wi-Fi or PC hotspot, scan the QR code shown
   in the PC browser, and enter the 10-digit PIN.
7. Upload or download from either device. Keep the NearbyPro.exe window open.
8. Press Ctrl+C in the terminal to stop the app.

FEATURES
- PC-to-mobile and mobile-to-PC transfer over LAN
- Numeric PIN login (spaces ignored)
- QR code connection (QR is generated locally, no online QR service)
- Multiple devices can connect concurrently
- Live upload progress with elapsed time and average transfer speed
- Download elapsed time, bytes transferred, percentage and average speed
- Image, video, audio and PDF previews
- Recent transfer history
- Download selected files as ZIP
- Dark/light theme
- Files stored in shared_files beside the executable
- 10 GB maximum total request size

DATA FILES
- config.json: persistent PIN
- shared_files: your uploaded files
- transfer_history.json: latest transfer activity

IMPORTANT
- Intended for a trusted home/private LAN only.
- Do NOT expose port 5000 to the internet or configure router port forwarding.
- Anyone on the reachable LAN with the PIN can upload, download and delete files.
- Stop the app when you finish sharing.
- This build is for 64-bit Windows. It does not install a service or run in background.
- If the phone cannot connect, check Windows Firewall (Private networks), ensure
  both devices are on the same network, and check that the router does not enable
  AP/client isolation.
